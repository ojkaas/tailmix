//go:build windows

package dns

import (
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"
	"testing"

	tailscaledns "tailscale.com/net/dns"
	"tailscale.com/util/dnsname"
)

type fakeRule struct {
	gp      bool
	servers []netip.Addr
	domains []string
}

type fakeNRPTStore struct {
	owned      []string
	rules      map[string]fakeRule
	foreignGP  bool
	nextID     int
	notifyGPs  int
	deletedIDs []string
}

func newFakeNRPTStore() *fakeNRPTStore { return &fakeNRPTStore{rules: map[string]fakeRule{}} }

func (s *fakeNRPTStore) ownedRuleIDs() []string { return slices.Clone(s.owned) }
func (s *fakeNRPTStore) setOwnedRuleIDs(ids []string) error {
	s.owned = slices.Clone(ids)
	return nil
}
func (s *fakeNRPTStore) hasForeignGPRules([]string) (bool, error) { return s.foreignGP, nil }
func (s *fakeNRPTStore) writeRule(id string, gp bool, servers []netip.Addr, domains []string) error {
	s.rules[id] = fakeRule{gp: gp, servers: slices.Clone(servers), domains: slices.Clone(domains)}
	return nil
}
func (s *fakeNRPTStore) deleteRule(id string) (bool, error) {
	rule, ok := s.rules[id]
	delete(s.rules, id)
	s.deletedIDs = append(s.deletedIDs, id)
	return ok && rule.gp, nil
}
func (s *fakeNRPTStore) newRuleID() (string, error) {
	s.nextID++
	return fmt.Sprintf("{rule-%d}", s.nextID), nil
}
func (s *fakeNRPTStore) notifyGP() error {
	s.notifyGPs++
	return nil
}

type fakeInterfaceDNS struct {
	servers []netip.Addr
	search  []string
	flushes int
}

func (f *fakeInterfaceDNS) setDNS(servers []netip.Addr, search []string) error {
	f.servers, f.search = slices.Clone(servers), slices.Clone(search)
	return nil
}
func (f *fakeInterfaceDNS) flushCaches() { f.flushes++ }

var resolver = netip.MustParseAddr("100.127.0.2")

func TestWindowsDNSSplitModeUsesNRPTOnly(t *testing.T) {
	store, iface := newFakeNRPTStore(), &fakeInterfaceDNS{}
	c, err := newWindowsConfigurator(t.Logf, store, iface)
	if err != nil {
		t.Fatal(err)
	}
	err = c.SetDNS(tailscaledns.OSConfig{
		Nameservers:   []netip.Addr{resolver},
		MatchDomains:  []dnsname.FQDN{"work.ts.net.", "home.ts.net."},
		SearchDomains: []dnsname.FQDN{"work.ts.net."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.rules) != 1 {
		t.Fatalf("rules = %v, want one", store.rules)
	}
	rule := store.rules[store.owned[0]]
	if want := []string{".work.ts.net", ".home.ts.net"}; !slices.Equal(rule.domains, want) {
		t.Fatalf("rule domains = %v, want %v", rule.domains, want)
	}
	if !slices.Equal(rule.servers, []netip.Addr{resolver}) || rule.gp {
		t.Fatalf("rule = %+v, want local rule to %v", rule, resolver)
	}
	if len(iface.servers) != 0 {
		t.Fatalf("adapter servers = %v, want none in split mode", iface.servers)
	}
	if !slices.Equal(iface.search, []string{"work.ts.net"}) {
		t.Fatalf("adapter search = %v", iface.search)
	}
}

func TestWindowsDNSPrimaryModeAddsCatchAll(t *testing.T) {
	store, iface := newFakeNRPTStore(), &fakeInterfaceDNS{}
	c, err := newWindowsConfigurator(t.Logf, store, iface)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetDNS(tailscaledns.OSConfig{Nameservers: []netip.Addr{resolver}}); err != nil {
		t.Fatal(err)
	}
	rule := store.rules[store.owned[0]]
	if !slices.Equal(rule.domains, []string{"."}) {
		t.Fatalf("rule domains = %v, want catch-all", rule.domains)
	}
	if !slices.Equal(iface.servers, []netip.Addr{resolver}) {
		t.Fatalf("adapter servers = %v, want %v", iface.servers, resolver)
	}
}

func TestWindowsDNSSplitsRulesAtFiftyDomains(t *testing.T) {
	store := newFakeNRPTStore()
	c, err := newWindowsConfigurator(t.Logf, store, &fakeInterfaceDNS{})
	if err != nil {
		t.Fatal(err)
	}
	var domains []dnsname.FQDN
	for i := range 120 {
		domains = append(domains, dnsname.FQDN(fmt.Sprintf("d%d.example.", i)))
	}
	if err := c.SetDNS(tailscaledns.OSConfig{Nameservers: []netip.Addr{resolver}, MatchDomains: domains}); err != nil {
		t.Fatal(err)
	}
	if len(store.owned) != 3 || len(store.rules) != 3 {
		t.Fatalf("owned = %v rules = %d, want 3", store.owned, len(store.rules))
	}
	total := 0
	for _, rule := range store.rules {
		if len(rule.domains) > nrptMaxDomains {
			t.Fatalf("rule has %d domains", len(rule.domains))
		}
		total += len(rule.domains)
	}
	if total != 120 {
		t.Fatalf("total domains = %d, want 120", total)
	}
	// Shrinking deletes surplus rules.
	if err := c.SetDNS(tailscaledns.OSConfig{Nameservers: []netip.Addr{resolver}, MatchDomains: domains[:10]}); err != nil {
		t.Fatal(err)
	}
	if len(store.owned) != 1 || len(store.rules) != 1 {
		t.Fatalf("owned = %v rules = %v, want one after shrink", store.owned, slices.Collect(maps.Keys(store.rules)))
	}
}

func TestWindowsDNSRemovesStaleOwnedRulesAtStartup(t *testing.T) {
	store := newFakeNRPTStore()
	store.owned = []string{"{stale}"}
	store.rules["{stale}"] = fakeRule{domains: []string{".old"}}
	store.rules["{official-client}"] = fakeRule{domains: []string{".ts.net"}}
	if _, err := newWindowsConfigurator(t.Logf, store, &fakeInterfaceDNS{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.rules["{stale}"]; ok {
		t.Fatal("stale tailmix rule survived startup")
	}
	if _, ok := store.rules["{official-client}"]; !ok {
		t.Fatal("startup removed a rule tailmix does not own")
	}
	if len(store.owned) != 0 {
		t.Fatalf("owned = %v, want none", store.owned)
	}
}

func TestWindowsDNSCloseRemovesEverything(t *testing.T) {
	store, iface := newFakeNRPTStore(), &fakeInterfaceDNS{}
	c, err := newWindowsConfigurator(t.Logf, store, iface)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetDNS(tailscaledns.OSConfig{Nameservers: []netip.Addr{resolver}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if len(store.rules) != 0 || len(store.owned) != 0 || len(iface.servers) != 0 {
		t.Fatalf("after Close rules=%v owned=%v servers=%v", store.rules, store.owned, iface.servers)
	}
	if err := c.SetDNS(tailscaledns.OSConfig{}); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("SetDNS after Close = %v, want closed error", err)
	}
}

func TestWindowsDNSWritesGroupPolicyWhenForeignGPRulesExist(t *testing.T) {
	store := newFakeNRPTStore()
	c, err := newWindowsConfigurator(t.Logf, store, &fakeInterfaceDNS{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := tailscaledns.OSConfig{Nameservers: []netip.Addr{resolver}, MatchDomains: []dnsname.FQDN{"a.ts.net."}}
	if err := c.SetDNS(cfg); err != nil {
		t.Fatal(err)
	}
	if store.rules[store.owned[0]].gp {
		t.Fatal("rule written as group policy without foreign GP rules")
	}
	store.foreignGP = true
	if err := c.SetDNS(cfg); err != nil {
		t.Fatal(err)
	}
	if len(store.rules) != 1 || !store.rules[store.owned[0]].gp {
		t.Fatalf("rules = %v, want a single group-policy rule", store.rules)
	}
	if store.notifyGPs == 0 {
		t.Fatal("group policy change was not notified")
	}
}
