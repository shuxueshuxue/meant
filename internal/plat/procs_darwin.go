//go:build darwin

package plat

import (
	"io"
	"os/exec"
	"strconv"
	"strings"
)

// Procs reads the process table through ps. `-E` appends the environment to
// the command column, but macOS withholds it for system binaries (sleep,
// python3 from /usr/bin, ...), so markers are a bonus here and ownership
// rests on the command's process group.
func Procs() map[int]Proc {
	out := map[int]Proc{}
	cmd := exec.Command("/bin/ps", "-axEww", "-o", "pid=,ppid=,pgid=,uid=,command=")
	stdout, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		return out
	}
	self := cmd.Process.Pid
	raw, _ := io.ReadAll(stdout)
	cmd.Wait()
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		p := Proc{}
		p.PID, _ = strconv.Atoi(f[0])
		p.PPID, _ = strconv.Atoi(f[1])
		p.PGID, _ = strconv.Atoi(f[2])
		p.UID, _ = strconv.Atoi(f[3])
		if p.PID == self {
			continue // the ps that is doing the looking
		}
		// The environment follows the arguments; the command ends at the
		// first NAME=value word.
		words, inEnv := []string{}, false
		for _, w := range f[4:] {
			inEnv = inEnv || looksLikeEnv(w)
			if v, ok := strings.CutPrefix(w, EnvSession+"="); ok {
				p.Session = v
			} else if v, ok := strings.CutPrefix(w, EnvCmd+"="); ok {
				p.CmdDir = v
			} else if !inEnv {
				words = append(words, w)
			}
		}
		p.Cmd = strings.Join(words, " ")
		out[p.PID] = p
	}
	return out
}

func looksLikeEnv(w string) bool {
	i := strings.IndexByte(w, '=')
	if i <= 0 {
		return false
	}
	for _, c := range w[:i] {
		if !(c == '_' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
