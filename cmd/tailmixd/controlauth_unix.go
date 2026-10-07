//go:build !windows

package main

import (
	"context"
	"net"
	"os"

	"github.com/tailscale/peercred"
	"tailscale.com/safesocket"
)

const mutationDeniedMessage = "tailmix management commands require root"

func controlPeerCredsAvailable() bool { return safesocket.PlatformUsesPeerCreds() }

// secureControlSocket lets every local user reach the socket when mutations
// are authorized by peer credentials, and only the owner otherwise.
func secureControlSocket(path string) error {
	mode := os.FileMode(0600)
	if safesocket.PlatformUsesPeerCreds() {
		mode = 0666
	}
	return os.Chmod(path, mode)
}

func removeControlSocket(path string) error { return os.Remove(path) }

func controlConnContext(ctx context.Context, conn net.Conn) context.Context {
	creds, err := peercred.Get(conn)
	if err != nil {
		return ctx
	}
	uid, ok := creds.UserID()
	if !ok {
		return ctx
	}
	return context.WithValue(ctx, peerUIDContextKey{}, uid)
}
