package dns

import (
	"errors"

	"golang.org/x/sys/windows"
)

const errAddrInUse = windows.WSAEADDRINUSE

// retryableTCPListenError reports whether binding TCP on UDP's ephemeral port
// failed for a reason another port avoids. Besides a port already in use,
// Windows refuses ports inside ranges excluded for Hyper-V, WSL or Docker
// with WSAEACCES; those ranges are per protocol, so a free UDP port can fall
// inside a reserved TCP range.
func retryableTCPListenError(err error) bool {
	return errors.Is(err, errAddrInUse) || errors.Is(err, windows.WSAEACCES)
}
