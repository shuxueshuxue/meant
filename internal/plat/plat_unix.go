//go:build unix

package plat

import (
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
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

// Who is the identity effects are judged against: the invoking user, even
// when the process has been elevated with sudo.
func Who() int {
	if s := os.Getenv("SUDO_UID"); s != "" && os.Geteuid() == 0 {
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
	}
	return os.Getuid()
}

// RealHome is the home directory from the account database. $HOME is not
// trusted: commands routinely repoint it at a temp dir and forget.
func RealHome() string {
	if u, err := user.LookupId(strconv.Itoa(Who())); err == nil && u.HomeDir != "" {
		return u.HomeDir
	}
	h, _ := os.UserHomeDir()
	return h
}

func UserName(uid int) string {
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		return u.Username
	}
	return "uid " + strconv.Itoa(uid)
}

func Facts(path string, fi fs.FileInfo) FileFacts {
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
	return Facts(dir, a).Dev != Facts(filepath.Dir(dir), b).Dev
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

// InGroup reports whether a process belongs to a process group.
func InGroup(p Proc, group int) bool { return group > 1 && p.PGID == group }

// JoinGroup is a no-op where the group is set at start (Setpgid).
func JoinGroup(pid int) {}

// Outlive does nothing on Unix: a process group lasts while any member does.
func Outlive(group int, procs map[int]Proc) {}

// NativePID is the identity here; see the Windows version.
func NativePID(pid int) int { return pid }

// Link makes dst an alias of the meant binary.
func Link(self, dst string) error { return os.Symlink(self, dst) }

func ShimName(name string) string { return name }
