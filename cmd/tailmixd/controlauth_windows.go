//go:build windows

package main

import (
	"context"
	"net"

	"github.com/maisem/tailmix/profilesocket"
	"golang.org/x/sys/windows"
)

const mutationDeniedMessage = "tailmix management commands require an Administrator account"

// controlPeerCredsAvailable is true on Windows: tailmix named pipes expose
// the client's access token.
func controlPeerCredsAvailable() bool { return true }

// secureControlSocket is a no-op for named pipes. profilesocket.Listen
// creates them with a security descriptor that admits local users, and
// mutations are authorized per connection from the client's token.
func secureControlSocket(string) error { return nil }

// removeControlSocket is a no-op: a named pipe disappears with its last
// handle.
func removeControlSocket(string) error { return nil }

// controlConnContext marks connections from privileged Windows users with
// the root UID so that requireRootForMutations admits them.
func controlConnContext(ctx context.Context, conn net.Conn) context.Context {
	clientConn, ok := conn.(*profilesocket.PipeConn)
	if !ok {
		return ctx
	}
	token, err := clientConn.Token()
	if err != nil {
		return ctx
	}
	defer token.Close()
	if !windowsTokenIsPrivileged(token) {
		return ctx
	}
	return context.WithValue(ctx, peerUIDContextKey{}, "0")
}

// windowsTokenIsPrivileged reports whether token belongs to LocalSystem or to
// a member of BUILTIN\Administrators.
//
// Membership counts even when UAC filtered the token and marked the group
// deny-only. That admits an administrator's ordinary, unelevated session,
// which is what lets the tray app manage tailmix without a UAC prompt,
// matching how the official Tailscale client is operated on Windows.
func windowsTokenIsPrivileged(token windows.Token) bool {
	user, err := token.GetTokenUser()
	if err != nil {
		return false
	}
	if user.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		return true
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false
	}
	groups, err := token.GetTokenGroups()
	if err != nil {
		return false
	}
	for _, group := range groups.AllGroups() {
		if group.Sid.Equals(admins) {
			return true
		}
	}
	return false
}
