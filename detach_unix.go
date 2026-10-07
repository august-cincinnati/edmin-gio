//go:build !windows

package main

import "syscall"

// detachedAttr starts the background copy in its own session, away from
// the terminal.
func detachedAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
