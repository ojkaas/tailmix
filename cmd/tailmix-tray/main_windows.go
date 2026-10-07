//go:build windows

// Command tailmix-tray is the tailmix notification-area app for Windows. It
// shows the state of every tailnet and offers the everyday controls of the
// official Tailscale client: connect, log in, exit nodes, device names.
package main

import (
	"errors"
	"flag"
	"log"
	"os"
	"path/filepath"

	"fyne.io/systray"
	"github.com/maisem/tailmix/controlapi"
	"github.com/maisem/tailmix/profilesocket"
	"golang.org/x/sys/windows"
)

func main() {
	socketDir := flag.String("socket-dir", profilesocket.DefaultDir(), "directory for the tailmix daemon socket")
	flag.Parse()

	setupLogging()
	release, ok := singleInstance()
	if !ok {
		return
	}
	defer release()

	t := newTray(controlapi.NewClient(*socketDir))
	systray.Run(t.onReady, t.onExit)
}

// setupLogging sends logs to %LOCALAPPDATA%\tailmix\tray.log; the tray has
// no console.
func setupLogging() {
	dir, err := os.UserCacheDir()
	if err != nil {
		return
	}
	dir = filepath.Join(dir, "tailmix")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return
	}
	path := filepath.Join(dir, "tray.log")
	if info, err := os.Stat(path); err == nil && info.Size() > 1<<20 {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return
	}
	log.SetOutput(f)
}

// singleInstance keeps one tray per user session.
func singleInstance() (func(), bool) {
	name, _ := windows.UTF16PtrFromString(`Local\tailmix-tray`)
	handle, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if handle != 0 {
			windows.CloseHandle(handle)
		}
		return nil, false
	}
	if err != nil {
		// Without the mutex we may run twice; better than not at all.
		log.Printf("single-instance mutex: %v", err)
		return func() {}, true
	}
	return func() { windows.CloseHandle(handle) }, true
}
