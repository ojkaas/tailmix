//go:build !windows

package dns

import (
	"errors"
	"syscall"
)

const errAddrInUse = syscall.EADDRINUSE

// retryableListenError reports whether binding the forwarder's port failed
// because the port is already occupied.
func retryableListenError(err error) bool {
	return errors.Is(err, errAddrInUse)
}
