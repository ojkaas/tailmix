//go:build windows

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/maisem/tailmix/controlapi"
	"golang.org/x/sys/windows"
)

func TestWindowsControlServerAuthorizesMutationsByToken(t *testing.T) {
	requirePipePrivileges(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &controlServerTestBackend{}
	dir := t.TempDir()
	server, err := startControlServer(ctx, dir, backend)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	client := controlapi.NewClient(dir)
	profiles, err := client.Profiles(ctx, false)
	if err != nil {
		t.Fatalf("read profiles: %v", err)
	}
	if len(profiles.Profiles) != 1 {
		t.Fatalf("profiles = %+v", profiles)
	}

	token := windows.GetCurrentProcessToken()
	privileged := windowsTokenIsPrivileged(token)
	_, err = client.AddProfile(ctx, controlapi.AddProfileRequest{Name: "home"})
	var apiErr *controlapi.Error
	switch {
	case privileged && err != nil:
		t.Fatalf("privileged mutation failed: %v", err)
	case !privileged && (!errors.As(err, &apiErr) || apiErr.Code != "permission_denied"):
		t.Fatalf("unprivileged mutation error = %v, want permission_denied", err)
	}
	t.Logf("test process privileged=%t", privileged)
}
