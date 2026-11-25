package verbs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"meant/internal/core"
	"meant/internal/judge"
	"meant/internal/plat"
)

// kill resolves the pids on its command line. By now `$(pgrep ...)`,
// `xargs` and friends have already produced the real numbers.
func kill(w *judge.World, argv []string) *core.Effect {
	args := argv[1:]
	if len(args) == 0 {
		return nil
	}
	sig := ""
	switch a := args[0]; {
	case a == "-l" || a == "-L" || a == "--list" || a == "--table" || a == "--version":
		return nil
	case a == "-s" || a == "-n" || a == "--signal":
		if len(args) < 2 {
			return nil
		}
		sig, args = args[1], args[2:]
	case a == "--":
		args = args[1:]
	case strings.HasPrefix(a, "-") && len(a) > 1:
		sig, args = a[1:], args[1:]
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if sig == "0" {
		return nil // a probe, not a signal
	}
	var pids []int
	for _, a := range args {
		n, err := strconv.Atoi(a)
		if err != nil {
			return nil // a name or job spec: not something this shim can resolve
		}
		pids = append(pids, n)
	}
	return w.Signal(quote(argv), pids)
}

// pkill and killall select by pattern. pgrep with the same selection says
// which processes the pattern matches right now.
func pkill(w *judge.World, argv []string) *core.Effect {
	pgrep := plat.LookReal("pgrep")
	if pgrep == "" {
		return nil
	}
	var sel []string
	if filepath.Base(argv[0]) == "killall" {
		_, _, names := operands(argv)
		for _, a := range argv[1:] {
			// Anything beyond a signal and plain names is beyond this model.
			if strings.HasPrefix(a, "-") && !isSignal(a[1:]) && a != "-s" && a != "-q" {
				return nil
			}
		}
		if len(names) != 1 {
			return nil
		}
		sel = []string{"-x", names[0]}
	} else {
		for i := 1; i < len(argv); i++ {
			a := argv[i]
			switch {
			case a == "--signal":
				i++
			case strings.HasPrefix(a, "--signal="), a == "-e", a == "--echo", a == "-q", a == "--queue":
			case strings.HasPrefix(a, "-") && isSignal(a[1:]):
			default:
				sel = append(sel, a)
			}
		}
	}
	out, err := exec.Command(pgrep, sel...).Output()
	if err != nil {
		return nil // no match, or a selection pgrep does not understand
	}
	var pids []int
	for _, f := range strings.Fields(string(out)) {
		if n, err := strconv.Atoi(f); err == nil && n != os.Getpid() {
			pids = append(pids, n)
		}
	}
	return w.Signal(quote(argv), pids)
}

func isSignal(s string) bool {
	if s == "" {
		return false
	}
	if _, err := strconv.Atoi(s); err == nil {
		return true
	}
	return s == strings.ToUpper(s) && strings.Trim(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZ12+-") == ""
}
