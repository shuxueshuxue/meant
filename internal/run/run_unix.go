//go:build unix

package run

import (
	"os"
	"os/exec"
	"syscall"
)

var forwarded = []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP}

func defaultShell(program string) (string, []string) {
	return "/bin/sh", []string{"-c", program}
}

// ownGroup gives the command its own process group, which is how its
// processes are recognised later on systems that hide their environment.
// With a terminal on stdin a new group would lose the right to read from it,
// so a person running `meant run` by hand keeps the plain arrangement.
func ownGroup(cmd *exec.Cmd) bool {
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 && fi.Mode()&os.ModeDevice != 0 {
		if _, err := os.Stat("/dev/null"); err == nil && !sameFile(fi, "/dev/null") {
			return false
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return true
}

func sameFile(fi os.FileInfo, path string) bool {
	other, err := os.Stat(path)
	return err == nil && os.SameFile(fi, other)
}
