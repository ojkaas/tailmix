package dns

import (
	"context"
	"net"
	"testing"

	"golang.org/x/sys/windows"
)

// TestListenForwarderRetriesExcludedTCPPort covers Windows hosts with Hyper-V,
// WSL or Docker, which reserve TCP port ranges: binding TCP on UDP's
// ephemeral port then fails with WSAEACCES instead of WSAEADDRINUSE.
func TestListenForwarderRetriesExcludedTCPPort(t *testing.T) {
	attempts := 0
	udp, tcp, err := listenForwarder(context.Background(), net.ListenUDP,
		func(network string, addr *net.TCPAddr) (*net.TCPListener, error) {
			attempts++
			if attempts == 1 {
				return nil, &net.OpError{Op: "listen", Net: network, Err: windows.WSAEACCES}
			}
			return net.ListenTCP(network, addr)
		})
	if err != nil {
		t.Fatalf("listenForwarder = %v, want a retry past the excluded port", err)
	}
	defer udp.Close()
	defer tcp.Close()
	if attempts != 2 {
		t.Fatalf("TCP attempts = %d, want 2", attempts)
	}
}
