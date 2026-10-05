package main

import (
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

// Pure-Go pseudo terminal support; openPty is per OS.

func ioctl(fd, req, arg uintptr) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg)
	if e != 0 {
		return e
	}
	return nil
}

type winsize struct {
	Rows, Cols, X, Y uint16
}

func setPtySize(f *os.File, rows, cols int) error {
	ws := winsize{Rows: uint16(rows), Cols: uint16(cols)}
	return ioctl(f.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws)))
}

// startShell launches argv (the user's shell if empty) attached to a new pty
// in dir.
func startShell(dir string, argv []string, rows, cols int) (*os.File, *exec.Cmd, error) {
	master, slave, err := openPty()
	if err != nil {
		return nil, nil, err
	}
	defer slave.Close()
	setPtySize(master, rows, cols)

	if len(argv) == 0 {
		argv = defaultShell()
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		master.Close()
		return nil, nil, err
	}
	return master, cmd, nil
}
