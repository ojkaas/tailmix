//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

// windowsServiceName is the Service Control Manager name that the installer
// registers tailmixd under.
const windowsServiceName = "tailmixd"

// maxServiceLogSize bounds the service log; a larger log is rotated to
// tailmixd.log.1 when the service starts.
const maxServiceLogSize = 10 << 20

// platformDataDir is where the service keeps its state and log.
func platformDataDir() string { return defaultDataDir() }

func defaultDataDir() string {
	if dir := os.Getenv("ProgramData"); dir != "" {
		return filepath.Join(dir, "tailmix")
	}
	return filepath.Join(`C:\ProgramData`, "tailmix")
}

// platformMain runs tailmixd under the Windows Service Control Manager when
// the SCM started the process. It reports whether it handled the run.
func platformMain(run func(ctx context.Context, stdout, stderr io.Writer) error) (bool, error) {
	isService, err := svc.IsWindowsService()
	if err != nil || !isService {
		return false, err
	}
	logFile, err := openServiceLog(filepath.Join(defaultDataDir(), "tailmixd.log"))
	if err != nil {
		return true, err
	}
	defer logFile.Close()
	handler := &windowsService{run: run, log: logFile}
	if err := svc.Run(windowsServiceName, handler); err != nil {
		fmt.Fprintf(logFile, "service run: %v\n", err)
		return true, err
	}
	return true, handler.err
}

type windowsService struct {
	run func(ctx context.Context, stdout, stderr io.Writer) error
	log io.Writer
	err error
}

func (s *windowsService) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.run(ctx, s.log, s.log) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	fmt.Fprintf(s.log, "%s tailmixd service started\n", time.Now().Format(time.RFC3339))
	for {
		select {
		case err := <-done:
			s.err = err
			if err != nil {
				fmt.Fprintf(s.log, "%s tailmixd exited: %v\n", time.Now().Format(time.RFC3339), err)
				// A non-zero exit code lets the SCM recovery policy restart us.
				return true, 1
			}
			return false, 0
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				status <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case s.err = <-done:
				case <-time.After(20 * time.Second):
					fmt.Fprintf(s.log, "%s tailmixd did not stop within 20s\n", time.Now().Format(time.RFC3339))
				}
				return false, 0
			}
		}
	}
}

func openServiceLog(path string) (*os.File, error) {
	if err := securePrivateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if info, err := os.Stat(path); err == nil && info.Size() > maxServiceLogSize {
		_ = os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
}

// checkPlatformPrivileges fails early when tailmixd lacks the rights its
// named pipes, Wintun adapter and DNS policy require.
func checkPlatformPrivileges() error {
	if windows.GetCurrentProcessToken().IsElevated() {
		return nil
	}
	return errors.New("tailmixd must run as Administrator or as the tailmixd Windows service")
}

// secureStateDir restricts the default data directory when the state file
// lives there. A state path elsewhere is left with the permissions its owner
// chose.
func secureStateDir(statePath string) error {
	dir, err := filepath.Abs(filepath.Dir(statePath))
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(dir), filepath.Clean(defaultDataDir())) {
		return nil
	}
	return securePrivateDir(dir)
}

// privateDirSDDL makes Administrators the owner, grants full control to
// LocalSystem and Administrators only, and blocks inherited permissions. The
// default ProgramData ACL would otherwise let every local user read node
// keys.
const privateDirSDDL = "O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

// securePrivateDir creates dir if needed and restricts it to privileged
// accounts. Files created inside inherit the restriction.
//
// Ordinary users may create folders in ProgramData. A directory that already
// exists must therefore be a real directory owned by Administrators or
// LocalSystem: one a user created in advance, or a junction, could let that
// user read node keys or redirect the service's writes as LocalSystem.
func securePrivateDir(dir string) error {
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	case err != nil:
		return err
	case !info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0:
		return fmt.Errorf("refusing to use %s: it is not a plain directory", dir)
	default:
		if err := checkTrustedOwner(dir); err != nil {
			return err
		}
	}
	sd, err := windows.SecurityDescriptorFromString(privateDirSDDL)
	if err != nil {
		return fmt.Errorf("parse private directory security descriptor: %w", err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("read private directory owner: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("read private directory DACL: %w", err)
	}
	err = windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner, nil, dacl, nil)
	if err != nil {
		return fmt.Errorf("restrict %s: %w", dir, err)
	}
	return nil
}

// checkTrustedOwner fails unless dir is owned by Administrators or
// LocalSystem.
func checkTrustedOwner(dir string) error {
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read owner of %s: %w", dir, err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("read owner of %s: %w", dir, err)
	}
	if owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) || owner.IsWellKnown(windows.WinLocalSystemSid) {
		return nil
	}
	return fmt.Errorf("refusing to use %s: it is owned by %v, not Administrators or LocalSystem; "+
		"if you did not create it, delete it and restart tailmixd", dir, owner)
}
