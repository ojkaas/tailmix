//go:build !windows

package profilesocket

import (
	"context"
	"net"

	"tailscale.com/safesocket"
)

// Listen creates a tailmix Unix socket. Access is governed by the socket
// file and its directory, as before the Windows port.
func Listen(path string) (net.Listener, error) {
	return safesocket.Listen(path)
}

// Dial connects to a tailmix Unix socket.
func Dial(ctx context.Context, path string) (net.Conn, error) {
	return safesocket.ConnectContext(ctx, path)
}
