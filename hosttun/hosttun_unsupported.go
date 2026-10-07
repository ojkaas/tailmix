//go:build !darwin && !linux && !windows

package hosttun

import (
	"errors"
)

func Open(OpenConfig) (Host, error) {
	return nil, errors.New("tailmix host TUN mode is currently implemented only on Darwin, Linux and Windows")
}
