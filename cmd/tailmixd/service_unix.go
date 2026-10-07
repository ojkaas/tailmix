//go:build !windows

package main

import (
	"context"
	"io"
)

// platformMain reports that the process runs as an ordinary command; service
// managers on Unix (systemd, launchd) need no in-process integration.
func platformMain(func(ctx context.Context, stdout, stderr io.Writer) error) (bool, error) {
	return false, nil
}

// platformDataDir returns "" so that the state path defaults to the user
// configuration directory.
func platformDataDir() string { return "" }

// checkPlatformPrivileges defers privilege checks on Unix to the operations
// that need them, which report root or CAP_NET_ADMIN requirements.
func checkPlatformPrivileges() error { return nil }

// secureStateDir is a no-op on Unix, where state files are created with
// owner-only permissions.
func secureStateDir(string) error { return nil }
