package profilesocket

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
)

// On Windows, sockets are named pipes and the socket "directory" is a prefix
// in the pipe namespace. safesocket listens on and dials these paths as pipes.
const (
	pipePrefix = `\\.\pipe\`
	defaultDir = pipePrefix + `tailmix`
)

// socketDir maps dir into the named-pipe namespace. A filesystem directory,
// such as one passed with -socket-dir or TAILMIX_SOCKET_DIR, becomes a stable
// pipe prefix derived from its absolute path, so independent daemons with
// different directories never share pipe names.
func socketDir(dir string) string {
	if strings.HasPrefix(strings.ToLower(dir), pipePrefix) {
		return dir
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(dir))))
	return fmt.Sprintf("%stailmix-%x", pipePrefix, sum[:8])
}
