//go:build linux || darwin

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

// ptyProc is a shell running in a pseudo terminal.
type ptyProc struct {
	master *os.File
	cmd    *exec.Cmd
}

func (p *ptyProc) Read(b []byte) (int, error)  { return p.master.Read(b) }
func (p *ptyProc) Write(b []byte) (int, error) { return p.master.Write(b) }
func (p *ptyProc) Wait() error                 { return p.cmd.Wait() }

// Resize tells the shell the terminal's new size.
func (p *ptyProc) Resize(rows, cols int) error {
	ws := winsize{Rows: uint16(rows), Cols: uint16(cols)}
	return ioctl(p.master.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws)))
}

// Kill ends the shell.
func (p *ptyProc) Kill() {
	p.cmd.Process.Signal(os.Interrupt)
	p.cmd.Process.Kill()
}

func (p *ptyProc) Close() error { return p.master.Close() }

// startShell launches argv (the user's shell if empty) attached to a new pty
// in dir.
func startShell(dir string, argv []string, rows, cols int) (*ptyProc, error) {
	master, slave, err := openPty()
	if err != nil {
		return nil, err
	}
	defer slave.Close()
	p := &ptyProc{master: master}
	p.Resize(rows, cols)

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
		return nil, err
	}
	p.cmd = cmd
	return p, nil
}
