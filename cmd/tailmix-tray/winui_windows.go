//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/atotto/clipboard"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	runKeyPath   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValueName = "tailmix-tray"
	appTitle     = "tailmix"
)

func shellOpen(target string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

// copyText places s on the clipboard as Unicode text.
func copyText(s string) error {
	return clipboard.WriteAll(s)
}

func messageBox(text string, flags uint32) int32 {
	textPtr, _ := windows.UTF16PtrFromString(text)
	titlePtr, _ := windows.UTF16PtrFromString(appTitle)
	ret, _ := windows.MessageBox(0, textPtr, titlePtr, flags|windows.MB_SETFOREGROUND|windows.MB_TOPMOST)
	return ret
}

func showError(text string) {
	go messageBox(text, windows.MB_OK|windows.MB_ICONERROR)
}

func confirm(text string) bool {
	const idOK = 1
	return messageBox(text, windows.MB_OKCANCEL|windows.MB_ICONQUESTION) == idOK
}

// inputBox prompts for a line of text. The standard library has no Win32
// input dialog, so this borrows the VB.NET InputBox through PowerShell.
func inputBox(title, prompt, defaultValue string) (string, bool) {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	script := "Add-Type -AssemblyName Microsoft.VisualBasic; " +
		"[Console]::OutputEncoding = [Text.Encoding]::UTF8; " +
		"$v = [Microsoft.VisualBasic.Interaction]::InputBox(" + quote(prompt) + ", " + quote(title) + ", " + quote(defaultValue) + "); " +
		"if ($v -eq '') { exit 1 }; [Console]::Out.Write($v)"
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

func runAtLoginEnabled() bool {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()
	_, _, err = key.GetStringValue(runValueName)
	return err == nil
}

func setRunAtLogin(enabled bool) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if !enabled {
		if err := key.DeleteValue(runValueName); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return key.SetStringValue(runValueName, `"`+exe+`"`)
}
