package profilesocket

import "path/filepath"

const ControlSocketName = "tailmixd.sock"

func ControlPath(dir string) string {
	return filepath.Join(socketDir(dir), ControlSocketName)
}
