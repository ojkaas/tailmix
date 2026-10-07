//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"
	"github.com/maisem/tailmix/controlapi"
	"github.com/maisem/tailmix/profilesocket"
	tailmixversion "github.com/maisem/tailmix/version"
	"tailscale.com/client/local"
	"tailscale.com/ipn"
)

const (
	pollInterval   = 3 * time.Second
	requestTimeout = 5 * time.Second
	maxPeerItems   = 30
	defaultAdmin   = "https://login.tailscale.com/admin/machines"
)

type peerItem struct {
	Label string
	Name  string // MagicDNS name, which resolves to the effective address
}

type tray struct {
	client *controlapi.Client

	mu         sync.Mutex
	model      trayModel
	peers      map[string][]peerItem
	signature  string
	iconKey    string
	menuCancel context.CancelFunc
	refreshCh  chan struct{}
}

func newTray(client *controlapi.Client) *tray {
	return &tray{client: client, refreshCh: make(chan struct{}, 1)}
}

func (t *tray) onReady() {
	systray.SetTitle("tailmix")
	t.refresh()
	go t.pollLoop()
}

func (t *tray) onExit() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.menuCancel != nil {
		t.menuCancel()
	}
}

// requestRefresh schedules an immediate status poll.
func (t *tray) requestRefresh() {
	select {
	case t.refreshCh <- struct{}{}:
	default:
	}
}

func (t *tray) pollLoop() {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
		case <-t.refreshCh:
		}
		t.refresh()
	}
}

func (t *tray) refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	status, err := t.client.Status(ctx)
	model := buildModel(status, err)
	peers := map[string][]peerItem{}
	for _, p := range model.Profiles {
		if p.Running && p.LocalAPI != "" {
			peers[p.Name] = fetchPeers(ctx, p.LocalAPI)
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.model = model
	t.peers = peers
	if key := fmt.Sprint(model.Icon, model.Lit); key != t.iconKey {
		if ico, err := encodeICO(renderIcon(model)); err == nil {
			systray.SetIcon(ico)
			t.iconKey = key
		}
	}
	systray.SetTooltip(model.Tooltip)
	sig := model.signature() + peersSignature(peers)
	if sig == t.signature {
		return
	}
	t.signature = sig
	t.rebuildLocked()
}

func fetchPeers(ctx context.Context, socket string) []peerItem {
	lc := localClient(socket)
	st, err := lc.Status(ctx)
	if err != nil || st == nil {
		return nil
	}
	var online, offline []peerItem
	for _, peer := range st.Peer {
		name := strings.TrimSuffix(peer.DNSName, ".")
		if name == "" {
			continue
		}
		short, _, _ := strings.Cut(name, ".")
		item := peerItem{Label: short, Name: name}
		if peer.Online {
			online = append(online, item)
		} else {
			item.Label += " (offline)"
			offline = append(offline, item)
		}
	}
	byLabel := func(a, b peerItem) int { return strings.Compare(a.Label, b.Label) }
	slices.SortFunc(online, byLabel)
	slices.SortFunc(offline, byLabel)
	return append(online, offline...)
}

func peersSignature(peers map[string][]peerItem) string {
	var b strings.Builder
	names := make([]string, 0, len(peers))
	for name := range peers {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		b.WriteString(name)
		for _, p := range peers[name] {
			b.WriteString("|" + p.Label)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// localClient talks to a profile's LocalAPI through a verified tailmix pipe.
func localClient(socket string) *local.Client {
	return &local.Client{
		Socket:        socket,
		UseSocketOnly: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return profilesocket.Dial(ctx, socket)
		},
	}
}

// rebuildLocked replaces the whole menu. Click handlers of the previous menu
// stop with its context.
func (t *tray) rebuildLocked() {
	if t.menuCancel != nil {
		t.menuCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.menuCancel = cancel
	m := t.model

	systray.ResetMenu()
	header := systray.AddMenuItem(m.Headline, "")
	header.Disable()

	if !m.Reachable {
		systray.AddSeparator()
		t.addSettings(ctx)
		return
	}
	if m.Up {
		t.onClick(ctx, systray.AddMenuItem("Disconnect", "Stop all tailnets"), func(ctx context.Context) error {
			_, err := t.client.SetDaemonUp(ctx, false)
			return err
		})
	} else {
		t.onClick(ctx, systray.AddMenuItem("Connect", "Start all enabled tailnets"), func(ctx context.Context) error {
			_, err := t.client.SetDaemonUp(ctx, true)
			return err
		})
	}
	systray.AddSeparator()

	for _, p := range m.Profiles {
		t.addProfileMenu(ctx, p, t.peers[p.Name])
	}
	if len(m.Profiles) > 0 {
		systray.AddSeparator()
	}
	if m.Up {
		t.addExitNodeMenu(ctx, m.Exit)
	}
	t.onClick(ctx, systray.AddMenuItem("Add tailnet…", "Log in to another tailnet"), t.addTailnet)
	systray.AddSeparator()
	t.addSettings(ctx)
}

func (t *tray) addProfileMenu(ctx context.Context, p profileModel, peers []peerItem) {
	menu := systray.AddMenuItem(p.Title, p.Detail)
	if p.Detail != "" {
		detail := menu.AddSubMenuItem(p.Detail, "")
		detail.Disable()
	}
	if p.LastError != "" {
		errItem := menu.AddSubMenuItem("Error: "+truncate(p.LastError, 80), p.LastError)
		errItem.Disable()
	}
	if p.Enabled && (p.NeedsLogin || p.State == "starting") {
		t.onClick(ctx, menu.AddSubMenuItem("Log in…", "Open the tailnet login page"), func(ctx context.Context) error {
			return t.login(ctx, p)
		})
	}
	if p.SelfIP.IsValid() {
		ip := p.SelfIP.String()
		t.onClick(ctx, menu.AddSubMenuItem("Copy my IP ("+ip+")", "This device's address in "+p.Name), func(context.Context) error {
			return copyText(ip)
		})
	}
	if p.SelfName != "" {
		name := p.SelfName
		t.onClick(ctx, menu.AddSubMenuItem("Copy my name ("+name+")", ""), func(context.Context) error {
			return copyText(name)
		})
	}
	if len(peers) > 0 {
		peerMenu := menu.AddSubMenuItem(fmt.Sprintf("Devices (%d)", len(peers)), "Click to copy a device's name")
		for i, peer := range peers {
			if i == maxPeerItems {
				more := peerMenu.AddSubMenuItem(fmt.Sprintf("… %d more", len(peers)-maxPeerItems), "")
				more.Disable()
				break
			}
			name := peer.Name
			t.onClick(ctx, peerMenu.AddSubMenuItem(peer.Label, name), func(context.Context) error {
				return copyText(name)
			})
		}
	}
	menu.AddSeparator()
	if p.Running && p.LocalAPI != "" {
		socket := p.LocalAPI
		t.onClick(ctx, menu.AddSubMenuItem("Admin console", ""), func(ctx context.Context) error {
			return openAdminConsole(ctx, socket)
		})
	}
	enabled := menu.AddSubMenuItemCheckbox("Enabled", "Connect to this tailnet while tailmix is up", p.Enabled)
	name := p.Name
	t.onClick(ctx, enabled, func(ctx context.Context) error {
		action := "enable"
		if p.Enabled {
			action = "disable"
		}
		_, err := t.client.ProfileAction(ctx, name, action)
		return err
	})
	if p.Enabled {
		t.onClick(ctx, menu.AddSubMenuItem("Restart", ""), func(ctx context.Context) error {
			_, err := t.client.ProfileAction(ctx, name, "restart")
			return err
		})
	}
	if p.Running && p.LocalAPI != "" {
		socket := p.LocalAPI
		t.onClick(ctx, menu.AddSubMenuItem("Log out", "Log this device out of "+name), func(ctx context.Context) error {
			if !confirm("Log out of " + name + "?\n\nThis device will leave the tailnet until you log in again.") {
				return nil
			}
			return localClient(socket).Logout(ctx)
		})
	}
}

func (t *tray) addExitNodeMenu(ctx context.Context, exit exitModel) {
	title := "Exit node: none"
	if exit.Selected != "" {
		title = "Exit node: " + exit.Selected
	}
	menu := systray.AddMenuItem(title, "Route all internet traffic through a tailnet device")
	none := menu.AddSubMenuItemCheckbox("None", "", exit.Selected == "")
	t.onClick(ctx, none, func(ctx context.Context) error {
		_, err := t.client.ClearExitNode(ctx)
		return err
	})
	if len(exit.Options) == 0 {
		empty := menu.AddSubMenuItem("No exit nodes available", "")
		empty.Disable()
		return
	}
	menu.AddSeparator()
	profiles, byProfile := exitOptionsByProfile(exit.Options)
	for _, profileName := range profiles {
		group := menu
		if len(profiles) > 1 {
			group = menu.AddSubMenuItem(profileName, "")
		}
		for _, option := range byProfile[profileName] {
			label := option.Label
			if !option.Online {
				label += " (offline)"
			}
			item := group.AddSubMenuItemCheckbox(label, "", option.Selected)
			if !option.Online && !option.Selected {
				item.Disable()
			}
			request := controlapi.SetExitNodeRequest{ProfileName: option.ProfileName, Peer: option.NodeID}
			t.onClick(ctx, item, func(ctx context.Context) error {
				_, err := t.client.SetExitNode(ctx, request)
				return err
			})
		}
	}
}

func (t *tray) addSettings(ctx context.Context) {
	runAtLogin := systray.AddMenuItemCheckbox("Run at login", "Start the tailmix tray when you sign in", runAtLoginEnabled())
	t.onClick(ctx, runAtLogin, func(context.Context) error {
		if err := setRunAtLogin(!runAtLoginEnabled()); err != nil {
			return err
		}
		if runAtLoginEnabled() {
			runAtLogin.Check()
		} else {
			runAtLogin.Uncheck()
		}
		return nil
	})
	about := systray.AddMenuItem("tailmix "+tailmixversion.GetMeta().Short, "")
	about.Disable()
	t.onClick(ctx, systray.AddMenuItem("Quit", "Close the tray app; tailmix keeps running"), func(context.Context) error {
		systray.Quit()
		return nil
	})
}

// onClick runs fn for each click on item until ctx ends. Errors are shown in
// a message box; every action is followed by a status refresh.
func (t *tray) onClick(ctx context.Context, item *systray.MenuItem, fn func(context.Context) error) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-item.ClickedCh:
				go func() {
					actionCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
					defer cancel()
					if err := fn(actionCtx); err != nil {
						log.Printf("action failed: %v", err)
						showError(describeError(err))
					}
					t.requestRefresh()
				}()
			}
		}
	}()
}

func describeError(err error) string {
	var apiErr *controlapi.Error
	if errors.As(err, &apiErr) && apiErr.Message != "" {
		return apiErr.Message
	}
	return err.Error()
}

// login opens the tailnet's login page, asking the profile engine for a new
// interactive login when it has not offered one yet.
func (t *tray) login(ctx context.Context, p profileModel) error {
	if p.AuthURL != "" {
		return openURL(p.AuthURL)
	}
	if p.LocalAPI == "" {
		return errors.New("profile " + p.Name + " is not running")
	}
	lc := localClient(p.LocalAPI)
	watcher, err := lc.WatchIPNBus(ctx, ipn.NotifyInitialState)
	if err != nil {
		return err
	}
	defer watcher.Close()
	if _, err := lc.EditPrefs(ctx, &ipn.MaskedPrefs{Prefs: ipn.Prefs{WantRunning: true}, WantRunningSet: true}); err != nil {
		return err
	}
	if err := lc.StartLoginInteractive(ctx); err != nil {
		return err
	}
	for {
		n, err := watcher.Next()
		if err != nil {
			return err
		}
		if n.BrowseToURL != nil && *n.BrowseToURL != "" {
			return openURL(*n.BrowseToURL)
		}
		if n.State != nil && *n.State == ipn.Running {
			return nil
		}
	}
}

// addTailnet asks for a profile name, adds the profile and opens its login
// page once the engine offers one.
func (t *tray) addTailnet(ctx context.Context) error {
	name, ok := inputBox("Add tailnet", "Name for the new tailnet profile (for example: work):", suggestProfileName(t.currentModel()))
	if !ok {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if !t.currentModel().Up {
		if _, err := t.client.SetDaemonUp(ctx, true); err != nil {
			return err
		}
	}
	if _, err := t.client.AddProfile(ctx, controlapi.AddProfileRequest{Name: name}); err != nil {
		return err
	}
	t.requestRefresh()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		profile, err := t.client.Profile(ctx, name)
		if err == nil {
			switch {
			case profile.AuthURL != "":
				return openURL(profile.AuthURL)
			case profile.RuntimeState == "running":
				return nil
			case profile.LocalAPISocket != "" && profile.RuntimeState == "needs-login":
				return t.login(ctx, profileModelFor(profile))
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("profile %q was added, but no login page was offered yet; use Log in… from its menu", name)
}

func (t *tray) currentModel() trayModel {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.model
}

func suggestProfileName(m trayModel) string {
	taken := map[string]bool{}
	for _, p := range m.Profiles {
		taken[p.Name] = true
	}
	for _, candidate := range []string{"work", "home", "lab"} {
		if !taken[candidate] {
			return candidate
		}
	}
	for i := 2; ; i++ {
		if candidate := fmt.Sprintf("tailnet%d", i); !taken[candidate] {
			return candidate
		}
	}
}

// openAdminConsole opens the admin console of the profile's control server.
func openAdminConsole(ctx context.Context, socket string) error {
	prefs, err := localClient(socket).GetPrefs(ctx)
	if err != nil {
		return err
	}
	target := defaultAdmin
	if control := prefs.ControlURL; control != "" && !ipn.IsLoginServerSynonym(control) {
		// Self-hosted control servers have no standard admin path.
		target = control
	}
	return openURL(target)
}

// openURL opens an http(s) URL in the default browser.
func openURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("refusing to open non-web URL %q", raw)
	}
	return shellOpen(u.String())
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
