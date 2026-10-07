package profilesocket

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"time"

	winio "github.com/tailscale/go-winio"
	"golang.org/x/sys/windows"
)

// pipeSDDL protects tailmix named pipes. Administrators own the pipe and
// LocalSystem and Administrators have full access. Local users may connect
// and read and write, but get 0x12019b (FILE_GENERIC_READ|FILE_GENERIC_WRITE
// without FILE_CREATE_PIPE_INSTANCE), so they cannot add instances of a
// running pipe and intercept clients. Tailscale's safesocket grants
// GENERIC_WRITE, which includes that right.
const pipeSDDL = "O:BAG:BAD:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x12019b;;;BU)"

var (
	advapi32                       = windows.NewLazySystemDLL("advapi32.dll")
	procImpersonateNamedPipeClient = advapi32.NewProc("ImpersonateNamedPipeClient")
)

// Listen creates a tailmix named pipe. Creating it requires an elevated
// Administrator or LocalSystem, because the pipe is owned by Administrators.
// It fails if any process already created a pipe with that name.
func Listen(path string) (net.Listener, error) {
	listener, err := winio.ListenPipe(path, &winio.PipeConfig{
		SecurityDescriptor: pipeSDDL,
		InputBufferSize:    256 * 1024,
		OutputBufferSize:   256 * 1024,
	})
	if err != nil {
		return nil, fmt.Errorf("listen on named pipe: %w", err)
	}
	return pipeListener{listener}, nil
}

type pipeListener struct{ net.Listener }

func (l pipeListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &PipeConn{Conn: conn}, nil
}

// PipeConn is a server-side tailmix pipe connection.
type PipeConn struct {
	net.Conn
}

// Token returns the client's access token, read at identification level.
// The caller must close it.
func (c *PipeConn) Token() (windows.Token, error) {
	handle, err := pipeHandle(c.Conn)
	if err != nil {
		return 0, err
	}
	// Impersonation changes thread-local state; keep it on this thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if r, _, err := procImpersonateNamedPipeClient.Call(uintptr(handle)); r == 0 {
		return 0, fmt.Errorf("impersonate pipe client: %w", err)
	}
	defer func() {
		if err := windows.RevertToSelf(); err != nil {
			panic(fmt.Errorf("revert pipe client impersonation: %w", err))
		}
	}()
	var token windows.Token
	if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &token); err != nil {
		return 0, fmt.Errorf("open pipe client token: %w", err)
	}
	return token, nil
}

// Dial connects to a tailmix named pipe and verifies that it belongs to
// tailmixd.
//
// Any local user can create a pipe with a free name, for example while the
// tailmixd service is stopped. Such a pipe would be owned by that user, who
// cannot make it owned by Administrators or LocalSystem. Dial therefore
// refuses pipes with any other owner before a request, which may carry auth
// keys or WireGuard secrets, is sent. The client's token is offered only at
// identification level, so a server can never impersonate the caller.
func Dial(ctx context.Context, path string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	conn, err := winio.DialPipeAccessImpLevel(ctx, path,
		windows.GENERIC_READ|windows.GENERIC_WRITE, winio.PipeImpLevelIdentification)
	if err != nil {
		return nil, err
	}
	if err := verifyPipeOwner(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("refusing tailmix pipe %s: %w", path, err)
	}
	return conn, nil
}

func verifyPipeOwner(conn net.Conn) error {
	handle, err := pipeHandle(conn)
	if err != nil {
		return err
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read pipe owner: %w", err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("read pipe owner: %w", err)
	}
	if !trustedPipeOwner(owner) {
		return fmt.Errorf("pipe is owned by %v, not Administrators or LocalSystem", owner)
	}
	return nil
}

func trustedPipeOwner(owner *windows.SID) bool {
	return owner != nil &&
		(owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) || owner.IsWellKnown(windows.WinLocalSystemSid))
}

func pipeHandle(conn net.Conn) (windows.Handle, error) {
	fd, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return 0, errors.New("connection is not a named pipe")
	}
	return windows.Handle(fd.Fd()), nil
}
