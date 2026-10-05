package main

import (
	"bytes"
	"os"
	"syscall"
	"unsafe"
)

// openPty opens a new master/slave pseudo terminal pair: the ioctl
// equivalents of posix_openpt, grantpt, unlockpt and ptsname.
func openPty() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	if err = ioctl(master.Fd(), syscall.TIOCPTYGRANT, 0); err != nil {
		master.Close()
		return nil, nil, err
	}
	if err = ioctl(master.Fd(), syscall.TIOCPTYUNLK, 0); err != nil {
		master.Close()
		return nil, nil, err
	}
	var name [128]byte
	if err = ioctl(master.Fd(), syscall.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))); err != nil {
		master.Close()
		return nil, nil, err
	}
	if i := bytes.IndexByte(name[:], 0); i >= 0 {
		slave, err = os.OpenFile(string(name[:i]), os.O_RDWR|syscall.O_NOCTTY, 0)
	} else {
		err = syscall.ENAMETOOLONG
	}
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}
