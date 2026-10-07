//go:build windows

package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"tailscale.com/ipn/ipnauth"
	"tailscale.com/ipn/ipnlocal"
	"tailscale.com/ipn/localapi"
	"tailscale.com/types/logger"
	"tailscale.com/types/logid"
)

// windowsProfileLocalAPIServer serves a profile's LocalAPI without
// ipnserver.
//
// On Windows, ipnserver treats LocalBackend as a multi-user service: a
// connecting user becomes the "current user", and LocalBackend switches to
// that user's Tailscale profile, falling back to an idle background profile
// when the last client disconnects. A tailmix engine owns exactly one
// profile that belongs to no Windows user, so every CLI or tray request
// would switch it to an empty, logged-out profile and back.
//
// Like tsnet's in-process LocalAPI, this server acts as the engine itself
// (ipnauth.Self) and authorizes per connection: everyone may read, and
// SYSTEM and members of Administrators may also write.
type windowsProfileLocalAPIServer struct {
	logf    logger.Logf
	backend *ipnlocal.LocalBackend
}

func newProfileLocalAPIServer(logf logger.Logf, backend *ipnlocal.LocalBackend) profileLocalAPIServer {
	return &windowsProfileLocalAPIServer{logf: logf, backend: backend}
}

type profileLocalAPIServer interface {
	Run(ctx context.Context, listener net.Listener) error
}

func (s *windowsProfileLocalAPIServer) Run(ctx context.Context, listener net.Listener) error {
	server := &http.Server{
		Handler:     http.HandlerFunc(s.serveHTTP),
		ConnContext: controlConnContext,
	}
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	err := server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (s *windowsProfileLocalAPIServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/localapi/") {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, "This is a tailmix profile LocalAPI.\n")
		return
	}
	privileged := r.Context().Value(peerUIDContextKey{}) == "0"
	handler := localapi.NewHandler(localapi.HandlerConfig{
		Actor:    ipnauth.Self,
		Backend:  s.backend,
		Logf:     s.logf,
		LogID:    logid.PublicID{},
		EventBus: s.backend.Sys().Bus.Get(),
	})
	handler.PermitRead = true
	handler.PermitWrite = privileged
	handler.PermitCert = privileged
	handler.ServeHTTP(w, r)
}
