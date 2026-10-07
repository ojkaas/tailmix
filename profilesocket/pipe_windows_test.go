package profilesocket

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	winio "github.com/tailscale/go-winio"
	"golang.org/x/sys/windows"
)

func testPipePath(t *testing.T) string {
	return fmt.Sprintf(`\\.\pipe\tailmix-test-%d-%s`, os.Getpid(), strings.ReplaceAll(t.Name(), "/", "-"))
}

// TestDialRefusesPipeOwnedByOrdinaryUser plays a local user who created a
// tailmix pipe name before tailmixd could, for example while the service was
// stopped. Without elevation the pipe is owned by the user, and Dial must
// refuse it before any request is sent.
func TestDialRefusesPipeOwnedByOrdinaryUser(t *testing.T) {
	if windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("an elevated test process owns pipes as Administrators")
	}
	path := testPipePath(t)
	squatter, err := winio.ListenPipe(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer squatter.Close()
	received := make(chan string, 1)
	go func() {
		conn, err := squatter.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		data, _ := io.ReadAll(conn)
		received <- string(data)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := Dial(ctx, path)
	if err == nil {
		conn.Close()
		t.Fatal("Dial accepted a pipe owned by an ordinary user")
	}
	if !strings.Contains(err.Error(), "not Administrators or LocalSystem") {
		t.Fatalf("Dial error = %v, want owner refusal", err)
	}
	select {
	case data := <-received:
		if data != "" {
			t.Fatalf("squatter received %q", data)
		}
	case <-time.After(3 * time.Second):
	}
}

// TestListenDialRoundTrip needs elevation: tailmix pipes are owned by
// Administrators, which only an elevated token may assign.
func TestListenDialRoundTrip(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("creating tailmix pipes requires an elevated process")
	}
	path := testPipePath(t)
	listener, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	tokenUser := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			tokenUser <- err.Error()
			return
		}
		defer conn.Close()
		token, err := conn.(*PipeConn).Token()
		if err != nil {
			tokenUser <- err.Error()
			return
		}
		defer token.Close()
		user, err := token.GetTokenUser()
		if err != nil {
			tokenUser <- err.Error()
			return
		}
		tokenUser <- user.User.Sid.String()
		conn.Write([]byte("ok"))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := Dial(ctx, path)
	if err != nil {
		t.Fatalf("Dial own pipe: %v", err)
	}
	defer conn.Close()
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil || string(reply) != "ok" {
		t.Fatalf("reply = %q, %v", reply, err)
	}
	self, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if got := <-tokenUser; got != self.User.Sid.String() {
		t.Fatalf("server saw client %s, want %s", got, self.User.Sid)
	}
}

// TestListenRejectsExistingPipe checks that tailmixd cannot be made to serve
// alongside a pipe someone else created first.
func TestListenRejectsExistingPipe(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("creating tailmix pipes requires an elevated process")
	}
	path := testPipePath(t)
	first, err := winio.ListenPipe(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := Listen(path); err == nil {
		second.Close()
		t.Fatal("Listen succeeded on a pipe name that already exists")
	}
}

func TestTrustedPipeOwner(t *testing.T) {
	for _, tc := range []struct {
		sid  windows.WELL_KNOWN_SID_TYPE
		want bool
	}{
		{windows.WinBuiltinAdministratorsSid, true},
		{windows.WinLocalSystemSid, true},
		{windows.WinBuiltinUsersSid, false},
		{windows.WinAuthenticatedUserSid, false},
		{windows.WinWorldSid, false},
	} {
		sid, err := windows.CreateWellKnownSid(tc.sid)
		if err != nil {
			t.Fatal(err)
		}
		if got := trustedPipeOwner(sid); got != tc.want {
			t.Errorf("trustedPipeOwner(%v) = %t, want %t", sid, got, tc.want)
		}
	}
	if trustedPipeOwner(nil) {
		t.Error("nil owner trusted")
	}
}
