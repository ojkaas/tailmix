package profilesocket

// On Windows the "socket directory" is a named-pipe namespace prefix;
// safesocket listens on and dials these paths as named pipes.
const defaultDir = `\\.\pipe\tailmix`
