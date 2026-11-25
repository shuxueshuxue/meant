//go:build unix

package plat

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

func stateRoot() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "meant")
	}
	return filepath.Join(RealHome(), ".local", "state", "meant")
}

func exeNames(name string) []string { return []string{name} }

func isExec(fi fs.FileInfo) bool { return fi.Mode()&0o111 != 0 }

func Facts(fi fs.FileInfo) FileFacts {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return FileFacts{UID: -1, Nlink: 1}
	}
	return FileFacts{
		UID:    int(st.Uid),
		Nlink:  uint64(st.Nlink),
		Dev:    uint64(st.Dev),
		Device: fi.Mode()&fs.ModeDevice != 0,
		Setuid: fi.Mode()&fs.ModeSetuid != 0,
	}
}

// IsFSRoot reports whether dir is the root of a mounted filesystem.
func IsFSRoot(dir string) bool {
	if filepath.Dir(dir) == dir {
		return true
	}
	a, err1 := os.Stat(dir)
	b, err2 := os.Stat(filepath.Dir(dir))
	if err1 != nil || err2 != nil || !a.IsDir() {
		return false
	}
	return Facts(a).Dev != Facts(b).Dev
}

// SystemDirs are trees whose ownership and modes the machine itself relies on.
func SystemDirs() []string {
	return []string{"/usr", "/etc", "/bin", "/sbin", "/lib", "/lib64", "/boot", "/dev", "/opt", "/var/lib",
		"/System", "/Library", "/private/etc", "/private/var/db"}
}

func TempDirs() []string {
	return []string{os.TempDir(), "/tmp", "/var/tmp", "/private/tmp"}
}

// Exec replaces this process with the real program, so a shim that lets a
// command through leaves no trace of itself in the process tree.
func Exec(path string, argv []string) error {
	return syscall.Exec(path, argv, os.Environ())
}

func Kill(pid int, hard bool) {
	sig := syscall.SIGTERM
	if hard {
		sig = syscall.SIGKILL
	}
	_ = syscall.Kill(pid, sig)
}

// KillGroup signals every process in a process group.
func KillGroup(pgid int, hard bool) {
	if pgid > 1 {
		Kill(-pgid, hard)
	}
}

// Link makes dst an alias of the meant binary.
func Link(self, dst string) error { return os.Symlink(self, dst) }

func ShimName(name string) string { return name }
