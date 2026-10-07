//go:build !windows

package main

import (
	"context"
	"net"

	"tailscale.com/ipn/ipnlocal"
	"tailscale.com/ipn/ipnserver"
	"tailscale.com/types/logger"
	"tailscale.com/types/logid"
)

type profileLocalAPIServer interface {
	Run(ctx context.Context, listener net.Listener) error
}

// newProfileLocalAPIServer serves a profile's LocalAPI with Tailscale's own
// ipnserver, which authorizes clients by Unix peer credentials.
func newProfileLocalAPIServer(logf logger.Logf, backend *ipnlocal.LocalBackend) profileLocalAPIServer {
	server := ipnserver.New(logf, logid.PublicID{}, backend.EventBus(), backend.NetMon())
	server.SetLocalBackend(backend)
	return server
}
