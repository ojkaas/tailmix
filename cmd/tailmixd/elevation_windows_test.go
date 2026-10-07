//go:build windows

package main

import (
	"testing"

	"golang.org/x/sys/windows"
)

// requirePipePrivileges skips tests that listen on tailmix named pipes from
// an unelevated process. safesocket creates pipes owned by Administrators,
// which Windows only permits for an elevated token.
func requirePipePrivileges(t *testing.T) {
	t.Helper()
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("listening on tailmix named pipes requires an elevated process")
	}
}
