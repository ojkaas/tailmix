//go:build !darwin && !linux && !windows

package dns

import "errors"

func StartService(ServiceConfig) (Service, error) {
	return nil, errors.New("tailmix MagicDNS OS integration is currently implemented only on Darwin, Linux and Windows")
}
