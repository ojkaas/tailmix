//go:build !windows

package profilesocket

const defaultDir = "/var/run/tailmix"

func socketDir(dir string) string { return dir }
