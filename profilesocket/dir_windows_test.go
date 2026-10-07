package profilesocket

import (
	"strings"
	"testing"
)

func TestWindowsSocketPathsAreNamedPipes(t *testing.T) {
	if got := ControlPath(DefaultDir()); got != `\\.\pipe\tailmix\tailmixd.sock` {
		t.Fatalf("default control path = %q", got)
	}
	profilePath, err := Path(DefaultDir(), "p_work")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(profilePath, `\\.\pipe\tailmix\`) {
		t.Fatalf("profile path = %q, want default pipe prefix", profilePath)
	}
}

func TestWindowsFilesystemSocketDirMapsToStablePipePrefix(t *testing.T) {
	a := ControlPath(`C:\Temp\run-a`)
	if !strings.HasPrefix(a, `\\.\pipe\tailmix-`) {
		t.Fatalf("control path = %q, want pipe namespace", a)
	}
	if again := ControlPath(`c:\temp\RUN-A\`); again != a {
		t.Fatalf("same directory mapped to %q and %q", a, again)
	}
	if other := ControlPath(`C:\Temp\run-b`); other == a {
		t.Fatalf("different directories share pipe %q", a)
	}
}
