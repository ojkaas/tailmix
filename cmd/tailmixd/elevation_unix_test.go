//go:build !windows

package main

import "testing"

func requirePipePrivileges(*testing.T) {}
