//go:build windows

package hosttun

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/tailscale/wireguard-go/tun"
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
	"tailscale.com/net/tsaddr"
	"tailscale.com/net/tstun"
	"tailscale.com/types/logger"
)

// tailmixAdapterGUID is the stable GUID requested for the tailmix Wintun
// adapter. It must differ from the official Tailscale client's adapter GUID
// so that both can run side by side, and it must stay stable so that Windows
// keeps per-interface settings across restarts.
var tailmixAdapterGUID = windows.GUID{
	Data1: 0x6d2bd7a3,
	Data2: 0x4f41,
	Data3: 0x4c55,
	Data4: [8]byte{0x9a, 0x2f, 0x74, 0xa1, 0xc3, 0xe0, 0xb6, 0xd1},
}

// windowsTunnelType is the Wintun tunnel type. Wintun derives the adapter
// description from it (tsconst.WintunInterfaceDesc), which is how Tailscale's netmon
// and netns code recognize virtual adapters that must never be selected as
// the underlay. Profile engines bind their sockets to the non-Tailscale
// default interface, which keeps tailmix's own exit routes from looping.
const windowsTunnelType = "Tailscale"

type windowsInstalledRoute struct {
	Destination netip.Prefix
	NextHop     netip.Addr
}

type windowsInstalledState struct {
	localAddrs []netip.Prefix
	routes     []windowsInstalledRoute
}

type windowsOps struct {
	addAddr func(netip.Prefix) error
	delAddr func(netip.Prefix) error
	// addRoute and delRoute manage a route on the tailmix adapter.
	addRoute func(destination netip.Prefix, nextHop netip.Addr) error
	delRoute func(destination netip.Prefix, nextHop netip.Addr) error
	// routeElsewhere reports whether another interface already carries a
	// route for exactly destination.
	routeElsewhere func(destination netip.Prefix) (bool, error)
}

type windowsHost struct {
	dev       tun.Device
	name      string
	luid      winipcfg.LUID
	logf      logger.Logf
	ops       windowsOps
	readState func() (windowsInstalledState, error)
	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

func Open(cfg OpenConfig) (Host, error) {
	if cfg.Name == "" {
		cfg.Name = "tailmix"
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	tun.WintunTunnelType = windowsTunnelType
	mtu := int(tstun.DefaultTUNMTU())
	dev, err := tun.CreateTUNWithRequestedGUID(cfg.Name, &tailmixAdapterGUID, mtu)
	if err != nil {
		return nil, fmt.Errorf("create Windows TUN %q (requires Administrator and wintun.dll next to tailmixd.exe): %w", cfg.Name, err)
	}
	nativeTun, ok := dev.(*tun.NativeTun)
	if !ok {
		_ = dev.Close()
		return nil, fmt.Errorf("create Windows TUN %q: unexpected device type %T", cfg.Name, dev)
	}
	name, err := dev.Name()
	if err != nil {
		_ = dev.Close()
		return nil, fmt.Errorf("read Windows TUN name: %w", err)
	}
	luid := winipcfg.LUID(nativeTun.LUID())
	h := &windowsHost{
		dev:  dev,
		name: name,
		luid: luid,
		logf: cfg.Logf,
		ops:  systemWindowsOps(luid),
	}
	h.readState = h.readInstalledState
	if err := h.prepareInterface(mtu); err != nil {
		_ = dev.Close()
		return nil, err
	}
	return h, nil
}

func (h *windowsHost) Device() tun.Device { return h.dev }
func (h *windowsHost) Name() string       { return h.name }

// prepareInterface waits for Windows to bring up the IP interfaces of a newly
// created adapter, then applies static per-interface settings.
func (h *windowsHost) prepareInterface(mtu int) error {
	var (
		ipif4 *winipcfg.MibIPInterfaceRow
		err   error
	)
	for range 50 {
		ipif4, err = h.luid.IPInterface(windows.AF_INET)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("Windows TUN %s has no IPv4 interface: %w", h.name, err)
	}
	ipif4.NLMTU = uint32(mtu)
	if err := ipif4.Set(); err != nil {
		return fmt.Errorf("configure Windows TUN %s IPv4 interface: %w", h.name, err)
	}
	ipif6, err := h.luid.IPInterface(windows.AF_INET6)
	if err != nil {
		// IPv6 can be disabled per adapter or by policy.
		h.logf("Windows TUN %s has no IPv6 interface: %v", h.name, err)
		return nil
	}
	ipif6.NLMTU = uint32(mtu)
	ipif6.DadTransmits = 0
	ipif6.RouterDiscoveryBehavior = winipcfg.RouterDiscoveryDisabled
	if err := ipif6.Set(); err != nil {
		return fmt.Errorf("configure Windows TUN %s IPv6 interface: %w", h.name, err)
	}
	return nil
}

func (h *windowsHost) Configure(cfg Config) error {
	localAddrs, routes, err := normalizeConfig(cfg)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errors.New("windows host TUN is closed")
	}
	installed, err := h.readState()
	if err != nil {
		return err
	}

	wantAddrs := make(map[netip.Addr]bool, len(localAddrs))
	for _, addr := range localAddrs {
		wantAddrs[addr.Addr()] = true
	}
	wantRoutes := make(map[netip.Prefix]windowsInstalledRoute, len(routes))
	var skipped []Route
	for _, route := range routes {
		if route.Optional {
			elsewhere, err := h.ops.routeElsewhere(route.Destination)
			if err != nil {
				h.logf("optional TUN route %v: cannot inspect routing table: %v", route.Destination, err)
			}
			if elsewhere {
				// Another adapter, such as the official Tailscale client,
				// already serves this destination. Do not shadow it.
				skipped = append(skipped, route)
				continue
			}
		}
		wantRoutes[route.Destination] = windowsInstalledRoute{
			Destination: route.Destination,
			NextHop:     windowsNextHop(route.Destination),
		}
	}

	for _, route := range installed.routes {
		if want, ok := wantRoutes[route.Destination]; ok && want == route {
			continue
		}
		if err := h.ops.delRoute(route.Destination, route.NextHop); err != nil && !isWindowsNotFound(err) {
			return fmt.Errorf("remove Windows TUN %s route %v: %w", h.name, route.Destination, err)
		}
	}
	currentAddrs := make(map[netip.Addr]bool, len(installed.localAddrs))
	for _, addr := range installed.localAddrs {
		if wantAddrs[addr.Addr()] {
			currentAddrs[addr.Addr()] = true
			continue
		}
		if err := h.ops.delAddr(addr); err != nil && !isWindowsNotFound(err) {
			return fmt.Errorf("remove Windows TUN %s address: %w", h.name, err)
		}
	}
	for _, addr := range localAddrs {
		if currentAddrs[addr.Addr()] {
			continue
		}
		if err := h.ops.addAddr(addr); err != nil && !errors.Is(err, windows.ERROR_OBJECT_ALREADY_EXISTS) {
			return fmt.Errorf("add Windows TUN %s address: %w", h.name, err)
		}
	}
	currentRoutes := make(map[windowsInstalledRoute]bool, len(installed.routes))
	for _, route := range installed.routes {
		currentRoutes[route] = true
	}
	for _, route := range routes {
		want, ok := wantRoutes[route.Destination]
		if !ok || currentRoutes[want] {
			continue
		}
		if route.Exit {
			h.logf("add aggregate exit route %v on %s", route.Destination, h.name)
		}
		if err := h.ops.addRoute(want.Destination, want.NextHop); err != nil && !errors.Is(err, windows.ERROR_OBJECT_ALREADY_EXISTS) {
			if route.Optional {
				h.logf("optional TUN route %v could not be installed: %v", route.Destination, err)
				continue
			}
			return fmt.Errorf("add Windows TUN %s route %v: %w", h.name, route.Destination, err)
		}
	}

	actual, err := h.readState()
	if err != nil {
		return err
	}
	if err := verifyHostConfig(localAddrs, routes, actual.localAddrs, windowsRoutes(actual, localAddrs)); err != nil {
		return fmt.Errorf("verify Windows TUN %s configuration: %w", h.name, err)
	}
	for _, route := range skipped {
		h.logf("optional TUN route %v is served by another interface; not installed", route.Destination)
	}
	return nil
}

// windowsRoutes converts installed routes into the platform-neutral form used
// by verifyHostConfig. Windows has no per-route preferred source, so the
// source is the adapter's only local address of the route's family.
func windowsRoutes(state windowsInstalledState, localAddrs []netip.Prefix) []Route {
	routes := make([]Route, 0, len(state.routes))
	for _, installed := range state.routes {
		route := Route{Destination: installed.Destination, Exit: isSplitDefault(installed.Destination)}
		for _, local := range localAddrs {
			if local.Addr().Is6() == installed.Destination.Addr().Is6() {
				route.Source = local.Addr()
				break
			}
		}
		routes = append(routes, route)
	}
	return routes
}

// windowsNextHop returns the next hop for a route on the tailmix adapter.
//
// Windows treats a route whose next hop is unspecified as on-link. For subnet
// routes, on-link semantics reserve the last address of the range as a
// broadcast address and refuse connections to it, so subnet routes use an
// arbitrary non-local next hop instead, as the official Tailscale client does.
// Host routes stay on-link.
func windowsNextHop(destination netip.Prefix) netip.Addr {
	if destination.IsSingleIP() {
		if destination.Addr().Is6() {
			return netip.IPv6Unspecified()
		}
		return netip.IPv4Unspecified()
	}
	if destination.Addr().Is6() {
		return tsaddr.TailscaleServiceIPv6()
	}
	return tsaddr.TailscaleServiceIP()
}

func isSplitDefault(prefix netip.Prefix) bool {
	return slices.Contains(splitDefaultPrefixes, prefix)
}

var splitDefaultPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/1"),
	netip.MustParsePrefix("128.0.0.0/1"),
	netip.MustParsePrefix("::/1"),
	netip.MustParsePrefix("8000::/1"),
}

func (h *windowsHost) readInstalledState() (windowsInstalledState, error) {
	var state windowsInstalledState
	for _, family := range []winipcfg.AddressFamily{windows.AF_INET, windows.AF_INET6} {
		addrs, err := winipcfg.GetUnicastIPAddressTable(family)
		if err != nil {
			return windowsInstalledState{}, fmt.Errorf("list Windows unicast addresses: %w", err)
		}
		for i := range addrs {
			row := &addrs[i]
			if row.InterfaceLUID != h.luid {
				continue
			}
			addr := row.Address.Addr()
			if !addr.IsValid() || addr.IsLinkLocalUnicast() {
				continue
			}
			state.localAddrs = append(state.localAddrs, netip.PrefixFrom(addr.Unmap(), addr.BitLen()))
		}
		routes, err := winipcfg.GetIPForwardTable2(family)
		if err != nil {
			return windowsInstalledState{}, fmt.Errorf("list Windows routes: %w", err)
		}
		for i := range routes {
			row := &routes[i]
			if row.InterfaceLUID != h.luid || row.Protocol != winipcfg.RouteProtocolNetMgmt {
				// Windows creates local, multicast and broadcast routes for
				// every adapter itself; only static routes are tailmix's.
				continue
			}
			state.routes = append(state.routes, windowsInstalledRoute{
				Destination: row.DestinationPrefix.Prefix(),
				NextHop:     row.NextHop.Addr(),
			})
		}
	}
	return state, nil
}

func systemWindowsOps(luid winipcfg.LUID) windowsOps {
	return windowsOps{
		addAddr: luid.AddIPAddress,
		delAddr: luid.DeleteIPAddress,
		addRoute: func(destination netip.Prefix, nextHop netip.Addr) error {
			return luid.AddRoute(destination, nextHop, 0)
		},
		delRoute: luid.DeleteRoute,
		routeElsewhere: func(destination netip.Prefix) (bool, error) {
			family := winipcfg.AddressFamily(windows.AF_INET)
			if destination.Addr().Is6() {
				family = windows.AF_INET6
			}
			routes, err := winipcfg.GetIPForwardTable2(family)
			if err != nil {
				return false, err
			}
			for i := range routes {
				if routes[i].InterfaceLUID != luid && routes[i].DestinationPrefix.Prefix() == destination {
					return true, nil
				}
			}
			return false, nil
		},
	}
}

func isWindowsNotFound(err error) bool {
	return errors.Is(err, windows.ERROR_NOT_FOUND) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND)
}

func (h *windowsHost) Close() error {
	h.closeOnce.Do(func() {
		h.mu.Lock()
		h.closed = true
		h.mu.Unlock()
		// Closing the Wintun device deletes the adapter, which removes its
		// addresses and routes with it.
		h.closeErr = h.dev.Close()
	})
	return h.closeErr
}
