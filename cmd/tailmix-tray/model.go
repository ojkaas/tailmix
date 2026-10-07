package main

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/maisem/tailmix/controlapi"
)

// iconState selects the tray icon.
type iconState int

const (
	iconOffline  iconState = iota // daemon unreachable or not running
	iconDown                      // daemon reachable but "tailmix down"
	iconStarting                  // profiles are starting or need login
	iconUp                        // every enabled profile is running
	iconError                     // a profile reports an error
)

// trayModel is the platform-neutral content of the tray icon and menu,
// derived from one daemon status snapshot.
type trayModel struct {
	Reachable bool
	Up        bool
	Icon      iconState
	// Lit reports, for the first three enabled profiles, whether each is
	// running. The icon lights one row of dots per running profile.
	Lit      [3]bool
	Tooltip  string
	Headline string
	Profiles []profileModel
	Exit     exitModel
}

type profileModel struct {
	Name       string
	Title      string // menu title, e.g. "work — Connected"
	Detail     string // tailnet suffix and address, when known
	State      string
	Enabled    bool
	Running    bool
	NeedsLogin bool
	AuthURL    string
	SelfIP     netip.Addr
	SelfName   string
	LocalAPI   string
	LastError  string
}

type exitModel struct {
	Selected     string // "" when no exit node is in use
	SelectedNode string
	State        string
	Options      []exitOption
}

type exitOption struct {
	ProfileName string
	NodeID      string
	Label       string
	Online      bool
	Selected    bool
}

func buildModel(status controlapi.Status, err error) trayModel {
	if err != nil {
		return trayModel{
			Icon:     iconOffline,
			Tooltip:  "tailmix: service not running",
			Headline: "tailmix service is not running",
		}
	}
	m := trayModel{Reachable: true, Up: status.State == "up"}
	running, enabled, lit := 0, 0, 0
	anyError, anyPending := false, false
	for _, p := range status.Profiles {
		if p.Removed {
			continue
		}
		pm := profileModelFor(p)
		m.Profiles = append(m.Profiles, pm)
		if !p.Enabled {
			continue
		}
		enabled++
		if pm.Running {
			running++
		}
		switch p.RuntimeState {
		case "error":
			anyError = true
		case "needs-login", "starting":
			anyPending = true
		}
		if lit < len(m.Lit) {
			m.Lit[lit] = pm.Running
			lit++
		}
	}
	m.Exit = exitModelFor(status.ExitNodes)

	switch {
	case !m.Up:
		m.Icon = iconDown
		m.Headline = "Disconnected"
	case anyError:
		m.Icon = iconError
		m.Headline = fmt.Sprintf("Connected to %d of %d tailnets", running, enabled)
	case anyPending || running < enabled:
		m.Icon = iconStarting
		m.Headline = fmt.Sprintf("Connected to %d of %d tailnets", running, enabled)
	case enabled == 0:
		m.Icon = iconStarting
		m.Headline = "No tailnets yet"
	default:
		m.Icon = iconUp
		m.Headline = fmt.Sprintf("Connected to %d %s", running, plural(running, "tailnet", "tailnets"))
	}
	m.Tooltip = "tailmix: " + m.Headline
	if m.Up && m.Exit.Selected != "" {
		m.Tooltip += "\nExit node: " + m.Exit.Selected
	}
	return m
}

func profileModelFor(p controlapi.Profile) profileModel {
	pm := profileModel{
		Name:       p.Name,
		State:      p.RuntimeState,
		Enabled:    p.Enabled,
		Running:    p.RuntimeState == "running",
		NeedsLogin: p.RuntimeState == "needs-login",
		AuthURL:    p.AuthURL,
		SelfName:   strings.TrimSuffix(p.SelfDNSName, "."),
		LocalAPI:   p.LocalAPISocket,
		LastError:  p.LastError,
	}
	for _, ip := range p.SelfIPs {
		if ip.Is4() {
			pm.SelfIP = ip
			break
		}
	}
	if !pm.SelfIP.IsValid() && len(p.SelfIPs) > 0 {
		pm.SelfIP = p.SelfIPs[0]
	}
	pm.Title = p.Name + " — " + stateLabel(p)
	var detail []string
	if suffix := strings.TrimSuffix(p.MagicDNSSuffix, "."); suffix != "" {
		detail = append(detail, suffix)
	}
	if pm.SelfIP.IsValid() {
		detail = append(detail, pm.SelfIP.String())
	}
	pm.Detail = strings.Join(detail, " · ")
	return pm
}

func stateLabel(p controlapi.Profile) string {
	if !p.Enabled {
		return "Disabled"
	}
	switch p.RuntimeState {
	case "running":
		return "Connected"
	case "needs-login":
		return "Needs login"
	case "starting":
		return "Starting…"
	case "down":
		return "Disconnected"
	case "error":
		return "Error"
	case "":
		return "Unknown"
	}
	return strings.ToUpper(p.RuntimeState[:1]) + p.RuntimeState[1:]
}

func exitModelFor(nodes controlapi.ExitNodes) exitModel {
	var em exitModel
	if sel := nodes.Selected; sel != nil {
		em.Selected = exitLabel(sel.ProfileName, sel.DNSName, sel.Location)
		em.SelectedNode = sel.ProfileName + "/" + sel.NodeID
		em.State = sel.State
	}
	for _, n := range nodes.Available {
		em.Options = append(em.Options, exitOption{
			ProfileName: n.ProfileName,
			NodeID:      n.NodeID,
			Label:       exitLabel("", n.DNSName, n.Location),
			Online:      n.Online,
			Selected:    em.SelectedNode == n.ProfileName+"/"+n.NodeID,
		})
	}
	sort.SliceStable(em.Options, func(i, j int) bool {
		a, b := em.Options[i], em.Options[j]
		if a.ProfileName != b.ProfileName {
			return a.ProfileName < b.ProfileName
		}
		if a.Online != b.Online {
			return a.Online
		}
		return a.Label < b.Label
	})
	return em
}

func exitLabel(profile, dnsName string, location *controlapi.ExitNodeLocation) string {
	name, _, _ := strings.Cut(strings.TrimSuffix(dnsName, "."), ".")
	if location != nil && location.City != "" {
		name = fmt.Sprintf("%s (%s, %s)", name, location.City, location.Country)
	}
	if profile != "" {
		return profile + " / " + name
	}
	return name
}

// exitOptionsByProfile groups exit node options by profile, keeping order.
func exitOptionsByProfile(options []exitOption) (profiles []string, byProfile map[string][]exitOption) {
	byProfile = map[string][]exitOption{}
	for _, o := range options {
		if _, ok := byProfile[o.ProfileName]; !ok {
			profiles = append(profiles, o.ProfileName)
		}
		byProfile[o.ProfileName] = append(byProfile[o.ProfileName], o)
	}
	return profiles, byProfile
}

// signature identifies the menu structure and labels. The tray rebuilds the
// menu only when it changes, so an open menu is not torn down needlessly.
func (m trayModel) signature() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%t|%t|%d|%v|%s\n", m.Reachable, m.Up, m.Icon, m.Lit, m.Headline)
	for _, p := range m.Profiles {
		fmt.Fprintf(&b, "%s|%s|%s|%t|%s|%s\n", p.Name, p.Title, p.Detail, p.Enabled, p.AuthURL, p.LastError)
	}
	fmt.Fprintf(&b, "%s|%s\n", m.Exit.SelectedNode, m.Exit.State)
	for _, o := range m.Exit.Options {
		fmt.Fprintf(&b, "%s|%s|%s|%t\n", o.ProfileName, o.NodeID, o.Label, o.Online)
	}
	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
