//go:build darwin || linux

package dns

import (
	"tailscale.com/control/controlknobs"
	"tailscale.com/health"
	tailscaledns "tailscale.com/net/dns"
	"tailscale.com/types/logger"
	"tailscale.com/util/eventbus"
)

func newPlatformOSConfigurator(logf logger.Logf, health *health.Tracker, bus *eventbus.Bus, knobs *controlknobs.Knobs, tunName string) (tailscaledns.OSConfigurator, error) {
	return tailscaledns.NewOSConfigurator(logf, health, bus, nil, knobs, tunName)
}
