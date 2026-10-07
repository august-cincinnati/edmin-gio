package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// Pseudo console support through ConPTY (Windows 10 1809 and later), which
// turns the console API calls of Windows programs into VT sequences.

var (
	kernel32                    = syscall.NewLazyDLL("kernel32.dll")
	procCreatePseudoConsole     = kernel32.NewProc("CreatePseudoConsole")
	procResizePseudoConsole     = kernel32.NewProc("ResizePseudoConsole")
	procClosePseudoConsole      = kernel32.NewProc("ClosePseudoConsole")
	procInitProcThreadAttrList  = kernel32.NewProc("InitializeProcThreadAttributeList")
	procUpdateProcThreadAttr    = kernel32.NewProc("UpdateProcThreadAttribute")
	procDeleteProcThreadAttrLst = kernel32.NewProc("DeleteProcThreadAttributeList")
)

const (
	procThreadAttributePseudoConsole = 0x00020016
	extendedStartupInfoPresent       = 0x00080000
	createUnicodeEnvironment         = 0x00000400
)

type startupInfoEx struct {
	syscall.StartupInfo
	attrList *byte
}

// coord packs a console size the way the API takes a COORD by value.
func coord(rows, cols int) uintptr {
	return uintptr(uint16(cols)) | uintptr(uint16(rows))<<16
}

// ptyProc is a shell running in a pseudo console.
type ptyProc struct {
	hpc     uintptr
	in, out *os.File // our ends: we write input to in and read output from out
	process syscall.Handle
	exited  chan struct{}

	closeConsole sync.Once
	closeAll     sync.Once
}

func (p *ptyProc) Read(b []byte) (int, error)  { return p.out.Read(b) }
func (p *ptyProc) Write(b []byte) (int, error) { return p.in.Write(b) }

// Wait waits for the shell to exit.
func (p *ptyProc) Wait() error {
	<-p.exited
	return nil
}

// Resize tells the shell the terminal's new size.
func (p *ptyProc) Resize(rows, cols int) error {
	if r, _, _ := procResizePseudoConsole.Call(p.hpc, coord(rows, cols)); r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

// Kill ends the shell.
func (p *ptyProc) Kill() { syscall.TerminateProcess(p.process, 1) }

// closePC closes the pseudo console, which ends the output pipe so that
// Read returns EOF. On older Windows it blocks until that output is read.
func (p *ptyProc) closePC() {
	p.closeConsole.Do(func() { procClosePseudoConsole.Call(p.hpc) })
}

func (p *ptyProc) Close() error {
	p.closeAll.Do(func() {
		go p.closePC()
		p.in.Close()
		p.out.Close()
		go func() {
			<-p.exited
			syscall.CloseHandle(p.process)
		}()
	})
	return nil
}

// startShell launches argv (the user's shell if empty) attached to a new
// pseudo console in dir.
func startShell(dir string, argv []string, rows, cols int) (*ptyProc, error) {
	if procCreatePseudoConsole.Find() != nil {
		return nil, errors.New("terminals need Windows 10 version 1809 or newer")
	}
	if len(argv) == 0 {
		argv = defaultShell()
	}
	exe, err := exec.LookPath(argv[0])
	if err != nil {
		return nil, err
	}

	var inR, inW, outR, outW syscall.Handle
	if err := syscall.CreatePipe(&inR, &inW, nil, 0); err != nil {
		return nil, err
	}
	if err := syscall.CreatePipe(&outR, &outW, nil, 0); err != nil {
		syscall.CloseHandle(inR)
		syscall.CloseHandle(inW)
		return nil, err
	}
	p := &ptyProc{
		in:     os.NewFile(uintptr(inW), "conpty-in"),
		out:    os.NewFile(uintptr(outR), "conpty-out"),
		exited: make(chan struct{}),
	}
	r, _, _ := procCreatePseudoConsole.Call(coord(rows, cols), uintptr(inR), uintptr(outW), 0, uintptr(unsafe.Pointer(&p.hpc)))
	// The pseudo console has its own copies of these.
	syscall.CloseHandle(inR)
	syscall.CloseHandle(outW)
	if r != 0 {
		p.in.Close()
		p.out.Close()
		return nil, syscall.Errno(r)
	}
	fail := func(err error) (*ptyProc, error) {
		p.closePC()
		p.in.Close()
		p.out.Close()
		return nil, err
	}

	var size uintptr
	procInitProcThreadAttrList.Call(0, 1, 0, uintptr(unsafe.Pointer(&size)))
	list := make([]uintptr, size/unsafe.Sizeof(uintptr(0))+1)
	if r, _, err := procInitProcThreadAttrList.Call(uintptr(unsafe.Pointer(&list[0])), 1, 0, uintptr(unsafe.Pointer(&size))); r == 0 {
		return fail(err)
	}
	defer procDeleteProcThreadAttrLst.Call(uintptr(unsafe.Pointer(&list[0])))
	if r, _, err := procUpdateProcThreadAttr.Call(uintptr(unsafe.Pointer(&list[0])), 0, procThreadAttributePseudoConsole,
		p.hpc, unsafe.Sizeof(p.hpc), 0, 0); r == 0 {
		return fail(err)
	}

	var si startupInfoEx
	si.Cb = uint32(unsafe.Sizeof(si))
	// Without explicit (invalid) standard handles, the shell would use
	// EdMin's own when they are redirected, instead of the pseudo console.
	si.Flags = syscall.STARTF_USESTDHANDLES
	si.StdInput, si.StdOutput, si.StdErr = syscall.InvalidHandle, syscall.InvalidHandle, syscall.InvalidHandle
	si.attrList = (*byte)(unsafe.Pointer(&list[0]))

	args := make([]string, len(argv))
	args[0] = syscall.EscapeArg(exe)
	for i, a := range argv[1:] {
		args[i+1] = syscall.EscapeArg(a)
	}
	cmdLine, err := syscall.UTF16PtrFromString(strings.Join(args, " "))
	if err != nil {
		return fail(err)
	}
	var dirp *uint16
	if dir != "" {
		if dirp, err = syscall.UTF16PtrFromString(dir); err != nil {
			return fail(err)
		}
	}
	env := envBlock(append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor"))
	var pi syscall.ProcessInformation
	err = syscall.CreateProcess(nil, cmdLine, nil, nil, false,
		extendedStartupInfoPresent|createUnicodeEnvironment, &env[0], dirp, &si.StartupInfo, &pi)
	if err != nil {
		return fail(err)
	}
	syscall.CloseHandle(pi.Thread)
	p.process = pi.Process
	go func() {
		syscall.WaitForSingleObject(p.process, syscall.INFINITE)
		close(p.exited)
		// Ending the pseudo console ends the output, so Read sees EOF.
		p.closePC()
	}()
	return p, nil
}

// envBlock encodes env for CreateProcess, later entries replacing earlier
// ones with the same (case-insensitive) name.
func envBlock(env []string) []uint16 {
	idx := map[string]int{}
	var kept []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv[min(1, len(kv)):], "=") // names like "=C:" start with '='
		k = strings.ToUpper(kv[:min(1, len(kv))] + k)
		if i, ok := idx[k]; ok {
			kept[i] = kv
			continue
		}
		idx[k] = len(kept)
		kept = append(kept, kv)
	}
	var b []uint16
	for _, kv := range kept {
		if strings.IndexByte(kv, 0) >= 0 {
			continue
		}
		b = append(b, utf16.Encode([]rune(kv))...)
		b = append(b, 0)
	}
	if len(b) == 0 {
		b = append(b, 0)
	}
	return append(b, 0)
}
