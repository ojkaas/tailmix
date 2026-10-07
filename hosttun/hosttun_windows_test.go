//go:build windows

package hosttun

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
)

// fakeWindowsSystem is an in-memory model of the tailmix adapter's addresses
// and static routes, plus routes owned by other interfaces.
type fakeWindowsSystem struct {
	addrs     []netip.Prefix
	routes    []windowsInstalledRoute
	elsewhere []netip.Prefix
	dropAdds  bool // simulate an add that reports success but does nothing
	calls     []string
}

func (f *fakeWindowsSystem) host() *windowsHost {
	h := &windowsHost{name: "tailmix", logf: func(string, ...any) {}}
	h.ops = windowsOps{
		addAddr: func(p netip.Prefix) error {
			f.calls = append(f.calls, "addAddr "+p.String())
			f.addrs = append(f.addrs, p)
			return nil
		},
		delAddr: func(p netip.Prefix) error {
			f.calls = append(f.calls, "delAddr "+p.String())
			f.addrs = slices.DeleteFunc(f.addrs, func(c netip.Prefix) bool { return c == p })
			return nil
		},
		addRoute: func(d netip.Prefix, nh netip.Addr) error {
			f.calls = append(f.calls, "addRoute "+d.String()+" via "+nh.String())
			if !f.dropAdds {
				f.routes = append(f.routes, windowsInstalledRoute{Destination: d, NextHop: nh})
			}
			return nil
		},
		delRoute: func(d netip.Prefix, nh netip.Addr) error {
			f.calls = append(f.calls, "delRoute "+d.String())
			f.routes = slices.DeleteFunc(f.routes, func(c windowsInstalledRoute) bool { return c.Destination == d && c.NextHop == nh })
			return nil
		},
		routeElsewhere: func(d netip.Prefix) (bool, error) {
			return slices.Contains(f.elsewhere, d), nil
		},
	}
	h.readState = func() (windowsInstalledState, error) {
		return windowsInstalledState{localAddrs: slices.Clone(f.addrs), routes: slices.Clone(f.routes)}, nil
	}
	return h
}

var (
	winNAT4  = netip.MustParseAddr("100.127.0.1")
	winNAT6  = netip.MustParseAddr("fd6d:6e65:7400::1")
	winLocal = []netip.Prefix{netip.PrefixFrom(winNAT4, 32), netip.PrefixFrom(winNAT6, 128)}
)

func TestWindowsConfigureInstallsAddressesAndRoutes(t *testing.T) {
	sys := &fakeWindowsSystem{}
	h := sys.host()
	cfg := Config{
		LocalAddrs: winLocal,
		Routes: []Route{
			{Destination: netip.MustParsePrefix("100.127.0.8/32"), Source: winNAT4},
			{Destination: netip.MustParsePrefix("10.20.0.0/16"), Source: winNAT4},
			{Destination: netip.MustParsePrefix("fd6d:6e65:7400::8/128"), Source: winNAT6},
		},
	}
	if err := h.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	want := []windowsInstalledRoute{
		{netip.MustParsePrefix("10.20.0.0/16"), netip.MustParseAddr("100.100.100.100")},
		{netip.MustParsePrefix("100.127.0.8/32"), netip.IPv4Unspecified()},
		{netip.MustParsePrefix("fd6d:6e65:7400::8/128"), netip.IPv6Unspecified()},
	}
	if !slices.Equal(sys.routes, want) {
		t.Fatalf("routes = %v, want %v", sys.routes, want)
	}
	if len(sys.addrs) != 2 {
		t.Fatalf("addrs = %v, want both NAT addresses", sys.addrs)
	}
}

func TestWindowsConfigureIsIdempotent(t *testing.T) {
	sys := &fakeWindowsSystem{}
	h := sys.host()
	cfg := Config{
		LocalAddrs: winLocal[:1],
		Routes:     []Route{{Destination: netip.MustParsePrefix("100.127.0.8/32"), Source: winNAT4}},
	}
	if err := h.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	sys.calls = nil
	if err := h.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if len(sys.calls) != 0 {
		t.Fatalf("second Configure made calls %v, want none", sys.calls)
	}
}

func TestWindowsConfigureRemovesStaleState(t *testing.T) {
	sys := &fakeWindowsSystem{
		addrs: []netip.Prefix{netip.MustParsePrefix("100.127.1.1/32")},
		routes: []windowsInstalledRoute{
			{netip.MustParsePrefix("100.127.1.9/32"), netip.IPv4Unspecified()},
		},
	}
	h := sys.host()
	cfg := Config{
		LocalAddrs: winLocal[:1],
		Routes:     []Route{{Destination: netip.MustParsePrefix("100.127.0.8/32"), Source: winNAT4}},
	}
	if err := h.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sys.addrs, winLocal[:1]) {
		t.Fatalf("addrs = %v, want %v", sys.addrs, winLocal[:1])
	}
	if len(sys.routes) != 1 || sys.routes[0].Destination != cfg.Routes[0].Destination {
		t.Fatalf("routes = %v, want only %v", sys.routes, cfg.Routes[0].Destination)
	}
}

func TestWindowsConfigureExitRoutes(t *testing.T) {
	sys := &fakeWindowsSystem{}
	h := sys.host()
	var routes []Route
	for _, p := range splitDefaultPrefixes {
		src := winNAT4
		if p.Addr().Is6() {
			src = winNAT6
		}
		routes = append(routes, Route{Destination: p, Source: src, Exit: true})
	}
	if err := h.Configure(Config{LocalAddrs: winLocal, Routes: routes}); err != nil {
		t.Fatal(err)
	}
	if len(sys.routes) != 4 {
		t.Fatalf("routes = %v, want four split default routes", sys.routes)
	}
	// Removing the exit node removes all four.
	if err := h.Configure(Config{LocalAddrs: winLocal}); err != nil {
		t.Fatal(err)
	}
	if len(sys.routes) != 0 {
		t.Fatalf("routes = %v, want none after clearing exit node", sys.routes)
	}
}

func TestWindowsConfigureDoesNotShadowOptionalRouteOwnedElsewhere(t *testing.T) {
	magicDNS := netip.MustParsePrefix("100.100.100.100/32")
	sys := &fakeWindowsSystem{elsewhere: []netip.Prefix{magicDNS}}
	h := sys.host()
	cfg := Config{
		LocalAddrs: winLocal[:1],
		Routes: []Route{
			{Destination: netip.MustParsePrefix("100.127.0.2/32"), Source: winNAT4},
			{Destination: magicDNS, Source: winNAT4, Optional: true},
		},
	}
	if err := h.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	for _, r := range sys.routes {
		if r.Destination == magicDNS {
			t.Fatalf("installed optional route %v that another interface owns", magicDNS)
		}
	}

	// Without another owner the optional route is installed.
	sys2 := &fakeWindowsSystem{}
	if err := sys2.host().Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(sys2.routes, func(r windowsInstalledRoute) bool { return r.Destination == magicDNS }) {
		t.Fatalf("routes = %v, want optional %v", sys2.routes, magicDNS)
	}
}

func TestWindowsConfigureRejectsRouteFalseSuccess(t *testing.T) {
	sys := &fakeWindowsSystem{dropAdds: true}
	h := sys.host()
	err := h.Configure(Config{
		LocalAddrs: winLocal[:1],
		Routes:     []Route{{Destination: netip.MustParsePrefix("100.127.0.8/32"), Source: winNAT4}},
	})
	if err == nil || !strings.Contains(err.Error(), "missing TUN route") {
		t.Fatalf("Configure error = %v, want missing route verification error", err)
	}
}

func TestWindowsAdapterGUIDDiffersFromTailscale(t *testing.T) {
	if got := tailmixAdapterGUID.String(); strings.EqualFold(got, "{37217669-42da-4657-a55b-0d995d328250}") {
		t.Fatal("tailmix adapter GUID collides with the official Tailscale adapter")
	}
}
