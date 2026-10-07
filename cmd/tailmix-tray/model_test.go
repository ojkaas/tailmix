package main

import (
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/maisem/tailmix/controlapi"
)

func TestBuildModelOffline(t *testing.T) {
	m := buildModel(controlapi.Status{}, errors.New("dial: no such pipe"))
	if m.Reachable || m.Icon != iconOffline {
		t.Fatalf("model = %+v, want offline", m)
	}
}

func TestBuildModelAllRunning(t *testing.T) {
	m := buildModel(controlapi.Status{
		State: "up",
		Profiles: []controlapi.Profile{
			{Name: "work", Enabled: true, RuntimeState: "running", MagicDNSSuffix: "work.ts.net.",
				SelfIPs: []netip.Addr{netip.MustParseAddr("fd7a:115c:a1e0::1"), netip.MustParseAddr("100.64.0.1")}},
			{Name: "home", Enabled: true, RuntimeState: "running"},
			{Name: "old", Removed: true, RuntimeState: "removed"},
		},
	}, nil)
	if m.Icon != iconUp || m.Headline != "Connected to 2 tailnets" {
		t.Fatalf("icon=%v headline=%q", m.Icon, m.Headline)
	}
	if len(m.Profiles) != 2 {
		t.Fatalf("profiles = %d, want removed profile hidden", len(m.Profiles))
	}
	if got := m.Profiles[0].Detail; got != "work.ts.net · 100.64.0.1" {
		t.Fatalf("detail = %q, want IPv4 preferred", got)
	}
	if m.Lit != [3]bool{true, true, false} {
		t.Fatalf("lit = %v", m.Lit)
	}
}

func TestBuildModelNeedsLogin(t *testing.T) {
	m := buildModel(controlapi.Status{
		State: "up",
		Profiles: []controlapi.Profile{
			{Name: "work", Enabled: true, RuntimeState: "running"},
			{Name: "home", Enabled: true, RuntimeState: "needs-login", AuthURL: "https://login.example/a/1"},
			{Name: "lab", Enabled: false, RuntimeState: "disabled"},
		},
	}, nil)
	if m.Icon != iconStarting || m.Headline != "Connected to 1 of 2 tailnets" {
		t.Fatalf("icon=%v headline=%q", m.Icon, m.Headline)
	}
	home := m.Profiles[1]
	if !home.NeedsLogin || home.Title != "home — Needs login" || home.AuthURL == "" {
		t.Fatalf("home = %+v", home)
	}
	if m.Profiles[2].Title != "lab — Disabled" {
		t.Fatalf("lab title = %q", m.Profiles[2].Title)
	}
}

func TestBuildModelDownAndError(t *testing.T) {
	down := buildModel(controlapi.Status{State: "down"}, nil)
	if down.Icon != iconDown || down.Headline != "Disconnected" {
		t.Fatalf("down model = %+v", down)
	}
	broken := buildModel(controlapi.Status{State: "up", Profiles: []controlapi.Profile{
		{Name: "work", Enabled: true, RuntimeState: "error", LastError: "boom"},
	}}, nil)
	if broken.Icon != iconError {
		t.Fatalf("icon = %v, want error", broken.Icon)
	}
}

func TestBuildModelExitNodes(t *testing.T) {
	m := buildModel(controlapi.Status{
		State: "up",
		ExitNodes: controlapi.ExitNodes{
			Selected: &controlapi.SelectedExitNode{ProfileName: "work", NodeID: "n2", DNSName: "ams.work.ts.net.", State: "active"},
			Available: []controlapi.AvailableExitNode{
				{ProfileName: "work", NodeID: "n1", DNSName: "zrh.work.ts.net.", Online: false},
				{ProfileName: "work", NodeID: "n2", DNSName: "ams.work.ts.net.", Online: true,
					Location: &controlapi.ExitNodeLocation{City: "Amsterdam", Country: "Netherlands"}},
				{ProfileName: "home", NodeID: "n3", DNSName: "pi.home.ts.net.", Online: true},
			},
		},
	}, nil)
	if m.Exit.Selected != "work / ams" {
		t.Fatalf("selected = %q", m.Exit.Selected)
	}
	if !strings.Contains(m.Tooltip, "Exit node: work / ams") {
		t.Fatalf("tooltip = %q", m.Tooltip)
	}
	profiles, byProfile := exitOptionsByProfile(m.Exit.Options)
	if strings.Join(profiles, ",") != "home,work" {
		t.Fatalf("profiles = %v", profiles)
	}
	work := byProfile["work"]
	if work[0].NodeID != "n2" || !work[0].Selected || work[0].Label != "ams (Amsterdam, Netherlands)" {
		t.Fatalf("work options = %+v, want online selected node first", work)
	}
}

func TestSignatureTracksVisibleChanges(t *testing.T) {
	base := controlapi.Status{State: "up", Profiles: []controlapi.Profile{{Name: "work", Enabled: true, RuntimeState: "starting"}}}
	a := buildModel(base, nil).signature()
	if b := buildModel(base, nil).signature(); a != b {
		t.Fatal("identical status produced different signatures")
	}
	base.Profiles[0].RuntimeState = "running"
	if c := buildModel(base, nil).signature(); c == a {
		t.Fatal("state change did not change the signature")
	}
}
