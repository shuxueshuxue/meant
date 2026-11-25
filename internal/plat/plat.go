// Package plat is the only place that knows which operating system this is.
// Everything above it asks questions in terms of processes, owners and
// filesystems and gets the same shape of answer on Linux, macOS and Windows.
package plat

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Markers are environment variables every process of a command inherits.
// They survive setsid, nohup and double forks, and need no privilege, which
// cgroups do. Not every system lets them be read back (macOS hides the
// environment of system binaries; Windows offers no cheap way at all), so
// each command also runs in its own process group, which survives nohup and
// `&` and can be read everywhere. A process is the session's own if either
// says so.
const (
	EnvSession = "MEANT_SESSION" // which agent session started this process
	EnvCmd     = "MEANT_CMD"     // state directory of the command it belongs to
	EnvRoot    = "MEANT_ROOT"    // pid of the command's runner
	EnvWS      = "MEANT_WS"      // workspace root
	EnvShims   = "MEANT_SHIMS"   // shim directory, first on PATH
)

type Proc struct {
	PID, PPID, PGID int
	UID             int
	Cmd             string // command line, for people
	Session         string // marker values, empty when unreadable
	CmdDir          string
}

// FileFacts are the properties of one file that decide who else is affected
// when it changes.
type FileFacts struct {
	UID    int
	Nlink  uint64
	Dev    uint64
	Device bool // character or block device
	Setuid bool
}

// StateRoot is where sessions, snapshots and shims live.
func StateRoot() string {
	if d := os.Getenv("MEANT_HOME"); d != "" {
		return d
	}
	return stateRoot()
}

// LookReal finds the real program behind a shim: the first match on PATH
// that is not meant itself. Identity is checked on the file, not on the
// directory name, so a shim directory that leaked onto some PATH without the
// rest of the environment can never resolve to itself and loop.
func LookReal(name string) string {
	var self os.FileInfo
	if exe, err := os.Executable(); err == nil {
		self, _ = os.Stat(exe)
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		for _, cand := range exeNames(name) {
			p := filepath.Join(dir, cand)
			fi, err := os.Stat(p)
			if err != nil || fi.IsDir() || !isExec(fi) || (self != nil && os.SameFile(fi, self)) {
				continue
			}
			return p
		}
	}
	return ""
}

// Descends reports whether pid is root or one of its descendants.
func Descends(procs map[int]Proc, pid, root int) bool {
	for hops := 0; pid > 1 && hops < 256; hops++ {
		if pid == root {
			return true
		}
		p, ok := procs[pid]
		if !ok {
			return false
		}
		pid = p.PPID
	}
	return false
}

// Abs resolves symlinks in the directory part and keeps the last component
// as named, which is how rm and friends see a path.
func Abs(path string) string {
	follow := strings.HasSuffix(path, "/") || strings.HasSuffix(path, string(os.PathSeparator))
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	if follow {
		if r, err := filepath.EvalSymlinks(abs); err == nil {
			return r
		}
		return abs
	}
	dir, base := filepath.Split(abs)
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Join(r, base)
	}
	return abs
}

// Resolve follows every symlink; used for the anchors paths are compared to.
func Resolve(path string) string {
	if path == "" {
		return ""
	}
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return filepath.Clean(path)
}

// Within reports whether path is root or inside it.
func Within(path, root string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// Self is this binary, with symlinks resolved: what aliases and harness
// settings point at.
func Self() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	return self, nil
}

// Lock takes a lock directory, serialising parallel tool calls. A lock older
// than stale belongs to a process that died and is taken over.
func Lock(path string, stale time.Duration) (unlock func()) {
	for i := 0; os.Mkdir(path, 0o755) != nil; i++ {
		if fi, err := os.Stat(path); i > 1000 || (err == nil && time.Since(fi.ModTime()) > stale) {
			os.Remove(path)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return func() { os.Remove(path) }
}
