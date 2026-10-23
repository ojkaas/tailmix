//go:build !windows

package dns

import (
	"errors"
	"syscall"
)

const errAddrInUse = syscall.EADDRINUSE

// retryableTCPListenError reports whether binding TCP on UDP's ephemeral port
// failed because the port is already occupied in the TCP port space.
func retryableTCPListenError(err error) bool {
	return errors.Is(err, errAddrInUse)
}
