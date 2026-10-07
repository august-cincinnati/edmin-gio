package main

import "syscall"

// detachedAttr starts the background copy without a console, so the one
// EdMin was started from (or the window Explorer opened for it) can close.
func detachedAttr() *syscall.SysProcAttr {
	const detachedProcess = 0x00000008
	return &syscall.SysProcAttr{CreationFlags: detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP}
}
