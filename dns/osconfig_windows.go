//go:build windows

package dns

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
	"tailscale.com/control/controlknobs"
	"tailscale.com/health"
	tailscaledns "tailscale.com/net/dns"
	"tailscale.com/types/logger"
	"tailscale.com/util/dnsname"
	"tailscale.com/util/eventbus"
	"tailscale.com/util/winutil/gp"
)

const platformGOOS = "windows"

// The NRPT (Name Resolution Policy Table) lives in these registry keys. Rules
// under the group-policy key take precedence: once any rule exists there,
// Windows ignores the local key entirely.
const (
	nrptBaseLocal = `SYSTEM\CurrentControlSet\Services\Dnscache\Parameters\DnsPolicyConfig`
	nrptBaseGP    = `SOFTWARE\Policies\Microsoft\Windows NT\DNSClient\DnsPolicyConfig`

	// tailmixRegKey records which NRPT rules tailmix owns. It is deliberately
	// separate from the official client's "SOFTWARE\Tailscale IPN" key:
	// Tailscale's own Windows DNS manager deletes every rule listed there at
	// startup, so sharing it would make the two clients erase each other.
	tailmixRegKey    = `SOFTWARE\tailmix`
	ownedRulesValue  = `NRPTRuleIDs`
	nrptMaxDomains   = 50 // NRPT rejects rules with more domains than this
	nrptOverrideDNS  = 0x8
	nrptRuleVersion  = 1
	nrptDomainsValue = `Name`
	nrptServersValue = `GenericDNSServers`
)

// nrptStore is the registry surface used by windowsConfigurator.
type nrptStore interface {
	ownedRuleIDs() []string
	setOwnedRuleIDs([]string) error
	hasForeignGPRules(owned []string) (bool, error)
	writeRule(id string, gp bool, servers []netip.Addr, domains []string) error
	deleteRule(id string) (deletedGP bool, err error)
	newRuleID() (string, error)
	notifyGP() error
}

// interfaceDNS is the per-adapter DNS surface used by windowsConfigurator.
type interfaceDNS interface {
	setDNS(servers []netip.Addr, searchDomains []string) error
	flushCaches()
}

// windowsConfigurator installs tailmix's MagicDNS configuration with
// tailmix-owned NRPT rules and the tailmix adapter's DNS settings. It never
// touches the official Tailscale client's rules, hosts-file section or
// registry state, so both can run on one machine.
type windowsConfigurator struct {
	logf  logger.Logf
	store nrptStore
	iface interfaceDNS

	mu      sync.Mutex
	ruleIDs []string
	gp      bool
	closed  bool
}

func newPlatformOSConfigurator(logf logger.Logf, _ *health.Tracker, _ *eventbus.Bus, _ *controlknobs.Knobs, tunName string) (tailscaledns.OSConfigurator, error) {
	ifc, err := net.InterfaceByName(tunName)
	if err != nil {
		return nil, fmt.Errorf("find Windows TUN %q: %w", tunName, err)
	}
	luid, err := winipcfg.LUIDFromIndex(uint32(ifc.Index))
	if err != nil {
		return nil, fmt.Errorf("find Windows TUN %q LUID: %w", tunName, err)
	}
	return newWindowsConfigurator(logf, registryNRPTStore{}, luidDNS{luid: luid, logf: logf})
}

func platformOSConfigurator(configurator tailscaledns.OSConfigurator) tailscaledns.OSConfigurator {
	return configurator
}

func newWindowsConfigurator(logf logger.Logf, store nrptStore, iface interfaceDNS) (*windowsConfigurator, error) {
	c := &windowsConfigurator{logf: logf, store: store, iface: iface}
	// NRPT rules survive an unclean exit. Remove whatever a previous run
	// left behind before installing anything new.
	c.ruleIDs = store.ownedRuleIDs()
	if err := c.writeRulesLocked(nil, nil); err != nil {
		return nil, fmt.Errorf("remove stale tailmix NRPT rules: %w", err)
	}
	return c, nil
}

func (c *windowsConfigurator) SetDNS(cfg tailscaledns.OSConfig) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("windows DNS configurator is closed")
	}
	search := make([]string, 0, len(cfg.SearchDomains))
	for _, domain := range cfg.SearchDomains {
		search = append(search, domain.WithoutTrailingDot())
	}

	var (
		ruleDomains   []string
		adapterServer []netip.Addr
	)
	switch {
	case len(cfg.Nameservers) == 0:
		// Nothing to resolve through tailmix; only search domains remain.
	case len(cfg.MatchDomains) == 0:
		// Primary mode: a catch-all NRPT rule makes the tailmix resolver
		// authoritative. Without it, Windows sends parallel queries to the
		// resolvers of every adapter and can leak names.
		ruleDomains = []string{"."}
		adapterServer = cfg.Nameservers
	default:
		// Split mode: route only the match domains. The adapter carries no
		// resolver of its own, so tailmix never becomes the primary.
		ruleDomains = nrptDomains(cfg.MatchDomains)
	}
	if err := c.writeRulesLocked(cfg.Nameservers, ruleDomains); err != nil {
		return err
	}
	if err := c.iface.setDNS(adapterServer, search); err != nil {
		return fmt.Errorf("set tailmix adapter DNS: %w", err)
	}
	c.iface.flushCaches()
	return nil
}

func (*windowsConfigurator) SupportsSplitDNS() bool { return true }

func (*windowsConfigurator) GetBaseConfig() (tailscaledns.OSConfig, error) {
	return tailscaledns.OSConfig{}, tailscaledns.ErrGetBaseConfigNotSupported
}

func (c *windowsConfigurator) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	err := errors.Join(c.writeRulesLocked(nil, nil), c.iface.setDNS(nil, nil))
	c.iface.flushCaches()
	return err
}

// writeRulesLocked makes the owned NRPT rules route domains to servers,
// splitting domains across rules as NRPT requires. Empty domains removes all
// owned rules.
func (c *windowsConfigurator) writeRulesLocked(servers []netip.Addr, domains []string) error {
	ruleCount := (len(domains) + nrptMaxDomains - 1) / nrptMaxDomains
	writeGP, err := c.store.hasForeignGPRules(c.ruleIDs)
	if err != nil {
		c.logf("inspect group-policy NRPT rules: %v", err)
	}
	notify := false
	// When the GP mode flips, rewrite every rule in the other location.
	if writeGP != c.gp {
		for _, id := range c.ruleIDs {
			deletedGP, err := c.store.deleteRule(id)
			if err != nil {
				return err
			}
			notify = notify || deletedGP
		}
		c.ruleIDs = nil
		c.gp = writeGP
	}
	for len(c.ruleIDs) < ruleCount {
		id, err := c.store.newRuleID()
		if err != nil {
			return err
		}
		c.ruleIDs = append(c.ruleIDs, id)
	}
	for _, id := range c.ruleIDs[ruleCount:] {
		deletedGP, err := c.store.deleteRule(id)
		if err != nil {
			return err
		}
		notify = notify || deletedGP
	}
	c.ruleIDs = c.ruleIDs[:ruleCount]
	if err := c.store.setOwnedRuleIDs(c.ruleIDs); err != nil {
		return fmt.Errorf("record tailmix NRPT rule IDs: %w", err)
	}
	for i, id := range c.ruleIDs {
		end := min((i+1)*nrptMaxDomains, len(domains))
		if err := c.store.writeRule(id, c.gp, servers, domains[i*nrptMaxDomains:end]); err != nil {
			return err
		}
	}
	if c.gp && ruleCount > 0 {
		notify = true
	}
	if notify {
		if err := c.store.notifyGP(); err != nil {
			c.logf("notify machine policy change: %v", err)
		}
	}
	return nil
}

// nrptDomains converts DNS suffixes to NRPT namespace syntax, which requires
// a leading dot.
func nrptDomains(domains []dnsname.FQDN) []string {
	out := make([]string, 0, len(domains))
	for _, domain := range domains {
		out = append(out, "."+domain.WithoutTrailingDot())
	}
	return out
}

type registryNRPTStore struct{}

func (registryNRPTStore) ownedRuleIDs() []string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, tailmixRegKey, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer key.Close()
	ids, _, err := key.GetStringsValue(ownedRulesValue)
	if err != nil {
		return nil
	}
	return ids
}

func (registryNRPTStore) setOwnedRuleIDs(ids []string) error {
	if len(ids) == 0 {
		key, err := registry.OpenKey(registry.LOCAL_MACHINE, tailmixRegKey, registry.SET_VALUE)
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		defer key.Close()
		if err := key.DeleteValue(ownedRulesValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil
	}
	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, tailmixRegKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.SetStringsValue(ownedRulesValue, ids)
}

func (registryNRPTStore) hasForeignGPRules(owned []string) (bool, error) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, nrptBaseGP, registry.READ)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer key.Close()
	names, err := key.ReadSubKeyNames(0)
	if err != nil {
		return false, err
	}
	for _, name := range names {
		mine := false
		for _, id := range owned {
			if strings.EqualFold(name, id) {
				mine = true
				break
			}
		}
		if !mine {
			return true, nil
		}
	}
	return false, nil
}

func (registryNRPTStore) writeRule(id string, gp bool, servers []netip.Addr, domains []string) error {
	base := nrptBaseLocal
	if gp {
		base = nrptBaseGP
	}
	path := base + `\` + id
	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, path, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open NRPT rule %s: %w", id, err)
	}
	defer key.Close()
	serverStrings := make([]string, 0, len(servers))
	for _, server := range servers {
		serverStrings = append(serverStrings, server.String())
	}
	return errors.Join(
		key.SetDWordValue("Version", nrptRuleVersion),
		key.SetStringsValue(nrptDomainsValue, domains),
		key.SetStringValue(nrptServersValue, strings.Join(serverStrings, ";")),
		key.SetDWordValue("ConfigOptions", nrptOverrideDNS),
	)
}

func (registryNRPTStore) deleteRule(id string) (bool, error) {
	if err := registry.DeleteKey(registry.LOCAL_MACHINE, nrptBaseLocal+`\`+id); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return false, fmt.Errorf("delete NRPT rule %s: %w", id, err)
	}
	err := registry.DeleteKey(registry.LOCAL_MACHINE, nrptBaseGP+`\`+id)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("delete group-policy NRPT rule %s: %w", id, err)
	}
	return true, nil
}

func (registryNRPTStore) newRuleID() (string, error) {
	guid, err := windows.GenerateGUID()
	if err != nil {
		return "", err
	}
	return guid.String(), nil
}

func (registryNRPTStore) notifyGP() error { return gp.NotifyMachinePolicyChange() }

type luidDNS struct {
	luid winipcfg.LUID
	logf logger.Logf
}

func (d luidDNS) setDNS(servers []netip.Addr, searchDomains []string) error {
	var v4, v6 []netip.Addr
	for _, server := range servers {
		if server.Is4() {
			v4 = append(v4, server)
		} else {
			v6 = append(v6, server)
		}
	}
	err4 := d.luid.SetDNS(windows.AF_INET, v4, searchDomains)
	err6 := d.luid.SetDNS(windows.AF_INET6, v6, searchDomains)
	if errors.Is(err6, windows.ERROR_NOT_FOUND) {
		// IPv6 is disabled on the adapter.
		err6 = nil
	}
	return errors.Join(err4, err6)
}

// flushCaches makes Windows notice the new adapter DNS settings and drops
// cached answers that the new configuration would route differently. Like
// the official client, it runs ipconfig asynchronously because it can take
// several seconds.
func (d luidDNS) flushCaches() {
	go func() {
		for _, arg := range []string{"/registerdns", "/flushdns"} {
			cmd := exec.Command("ipconfig", arg)
			cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}
			if err := cmd.Run(); err != nil {
				d.logf("ipconfig %s: %v", arg, err)
			}
		}
	}()
}
