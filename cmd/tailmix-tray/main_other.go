//go:build !windows

// Command tailmix-tray is the tailmix notification-area app for Windows.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "tailmix-tray is only available on Windows; use the tailmix CLI on this platform")
	os.Exit(1)
}
