package main

import (
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Choosing the shell a project's terminals run: the project's "shell"
// setting, else one detected from where the project lives, else $SHELL
// (PowerShell on Windows).

// shellEnv is what detectShell looks at, gathered by hostShellEnv; tests
// supply their own.
type shellEnv struct {
	inWSL     bool
	mountinfo string // contents of /proc/self/mountinfo
	lookPath  func(string) (string, error)
}

func hostShellEnv() shellEnv {
	env := shellEnv{lookPath: exec.LookPath}
	if _, err := os.Stat("/proc/sys/fs/binfmt_misc/WSLInterop"); err == nil {
		env.inWSL = true
	} else if v, err := os.ReadFile("/proc/version"); err == nil {
		env.inWSL = strings.Contains(strings.ToLower(string(v)), "microsoft")
	}
	if data, err := os.ReadFile("/proc/self/mountinfo"); err == nil {
		env.mountinfo = string(data)
	}
	return env
}

// projectShell returns the command for root's terminals; nil means $SHELL.
func projectShell(root string) []string {
	if s := loadSettings(projectSettingsPath(root)).Shell; len(s) > 0 {
		return s
	}
	return detectShell(root, hostShellEnv())
}

// defaultShell is the user's login shell. On Windows it is PowerShell (7 if
// installed), else cmd.exe; $SHELL there is usually an MSYS path that only
// Git Bash understands.
func defaultShell() []string {
	if runtime.GOOS == "windows" {
		for _, sh := range []string{"pwsh.exe", "powershell.exe"} {
			if p, err := exec.LookPath(sh); err == nil {
				return []string{p, "-NoLogo"}
			}
		}
		if sh := os.Getenv("ComSpec"); sh != "" {
			return []string{sh}
		}
		return []string{"cmd.exe"}
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return []string{sh}
	}
	return []string{"/bin/sh"}
}

type mount struct {
	point, fstype, source, opts string
}

// parseMountinfo reads the mount point, filesystem type, source and
// filesystem options from each line of /proc/self/mountinfo.
func parseMountinfo(text string) []mount {
	var ms []mount
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		// Fields: id parent dev root point opts [optional…] - fstype source superopts
		sep := -1
		for i := 6; i < len(f); i++ {
			if f[i] == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || sep+2 >= len(f) {
			continue
		}
		m := mount{point: unescapeMount(f[4]), fstype: f[sep+1], source: unescapeMount(f[sep+2])}
		if sep+3 < len(f) {
			m.opts = f[sep+3]
		}
		ms = append(ms, m)
	}
	return ms
}

var octalEscape = regexp.MustCompile(`\\[0-7]{3}`)

// unescapeMount decodes the \040-style escapes mountinfo uses for spaces,
// tabs, newlines and backslashes.
func unescapeMount(s string) string {
	return octalEscape.ReplaceAllStringFunc(s, func(e string) string {
		n := (e[1]-'0')*64 + (e[2]-'0')*8 + (e[3] - '0')
		return string([]byte{n})
	})
}

// mountFor returns the innermost mount containing p.
func mountFor(p string, ms []mount) (mount, bool) {
	var best mount
	found := false
	for _, m := range ms {
		if (p == m.point || strings.HasPrefix(p, strings.TrimSuffix(m.point, "/")+"/")) &&
			(!found || len(m.point) > len(best.point)) {
			best, found = m, true
		}
	}
	return best, found
}

var windowsDrive = regexp.MustCompile(`^/mnt/[a-zA-Z](/|$)`)

// detectShell picks a shell for a project at root from where it lives, or
// returns nil for the default:
//   - a Windows drive under WSL gets PowerShell;
//   - a folder on an sshfs mount gets ssh to that host, in the same folder.
func detectShell(root string, env shellEnv) []string {
	m, mounted := mountFor(root, parseMountinfo(env.mountinfo))
	if mounted && m.fstype == "fuse.sshfs" {
		return sshShell(root, m)
	}
	onDrive := windowsDrive.MatchString(root) ||
		mounted && (m.fstype == "drvfs" || strings.Contains(m.opts, "aname=drvfs"))
	if env.inWSL && onDrive {
		// WSL runs Windows programs in the matching Windows folder.
		for _, ps := range []string{"pwsh.exe", "powershell.exe"} {
			if p, err := env.lookPath(ps); err == nil {
				return []string{p, "-NoLogo"}
			}
		}
		const builtin = "/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe"
		if _, err := os.Stat(builtin); err == nil {
			return []string{builtin, "-NoLogo"}
		}
	}
	return nil
}

// sshShell opens a login shell on an sshfs mount's host, in the folder that
// root corresponds to there.
func sshShell(root string, m mount) []string {
	// The source is [user@]host:[dir]; host may be an [IPv6] address.
	src := m.source
	hostEnd := strings.Index(src, ":")
	if i := strings.Index(src, "]"); i >= 0 && i < len(src)-1 && src[i+1] == ':' {
		hostEnd = i + 1
	}
	if hostEnd < 0 {
		return nil
	}
	host := strings.NewReplacer("[", "", "]", "").Replace(src[:hostEnd])
	remote := src[hostEnd+1:]
	rel, err := filepath.Rel(m.point, root)
	if err != nil {
		return nil
	}
	// An empty or relative remote dir is relative to the remote home, which
	// is where ssh starts.
	dir := path.Join(remote, filepath.ToSlash(rel))
	return []string{"ssh", "-t", host, "cd " + shQuote(dir) + " && exec $SHELL -l"}
}

// shQuote quotes s for a POSIX shell.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
