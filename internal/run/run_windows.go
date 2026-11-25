//go:build windows

package run

import (
	"os"
	"os/exec"

	"meant/internal/plat"
)

var forwarded = []os.Signal{os.Interrupt}

// defaultShell prefers a POSIX shell (Git Bash is what agent harnesses use
// on Windows) and falls back to cmd.
func defaultShell(program string) (string, []string) {
	if bash := plat.LookReal("bash"); bash != "" {
		return bash, []string{"-c", program}
	}
	return os.Getenv("ComSpec"), []string{"/D", "/S", "/C", program}
}

func ownGroup(cmd *exec.Cmd) bool { return false }
