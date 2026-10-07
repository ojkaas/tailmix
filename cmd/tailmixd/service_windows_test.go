//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestSecurePrivateDirRefusesUserOwnedDirectory plays a local user who
// created the data directory before the service did.
func TestSecurePrivateDirRefusesUserOwnedDirectory(t *testing.T) {
	if windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("an elevated test process creates directories owned by Administrators")
	}
	dir := filepath.Join(t.TempDir(), "tailmix")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	err := securePrivateDir(dir)
	if err == nil || !strings.Contains(err.Error(), "not Administrators or LocalSystem") {
		t.Fatalf("securePrivateDir = %v, want owner refusal", err)
	}
}

func TestSecurePrivateDirRefusesJunction(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "tailmix")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("cannot create junction: %v: %s", err, out)
	}
	err := securePrivateDir(link)
	if err == nil || !strings.Contains(err.Error(), "not a plain directory") {
		t.Fatalf("securePrivateDir = %v, want junction refusal", err)
	}
}

func TestSecurePrivateDirRestrictsNewDirectory(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("assigning Administrators as owner requires an elevated process")
	}
	dir := filepath.Join(t.TempDir(), "tailmix")
	if err := securePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil || !owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		t.Fatalf("owner = %v, %v; want Administrators", owner, err)
	}
	if got := sd.String(); strings.Contains(got, ";;;BU)") || strings.Contains(got, ";;;AU)") || strings.Contains(got, ";;;OW)") {
		t.Fatalf("security descriptor %s grants access beyond SYSTEM and Administrators", got)
	}
	// Securing again is idempotent.
	if err := securePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
}
