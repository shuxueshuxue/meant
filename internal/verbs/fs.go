package verbs

import (
	"bytes"
	"os"
	"os/exec"
	"strings"

	"meant/internal/core"
	"meant/internal/judge"
	"meant/internal/plat"
)

// operands splits argv into single-letter flags, long flags and operands,
// honouring `--`. It is enough for the coreutils-style programs modelled here.
func operands(argv []string) (flags string, long []string, rest []string) {
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--":
			return flags, long, append(rest, argv[i+1:]...)
		case strings.HasPrefix(a, "--"):
			long = append(long, a)
		case strings.HasPrefix(a, "-") && len(a) > 1:
			flags += a[1:]
		default:
			rest = append(rest, a)
		}
	}
	return
}

func has(long []string, name string) bool {
	for _, l := range long {
		if l == name || strings.HasPrefix(l, name+"=") {
			return true
		}
	}
	return false
}

func existing(paths []string) []string {
	var out []string
	for _, p := range paths {
		abs := plat.Abs(p)
		if _, err := os.Lstat(abs); err == nil {
			out = append(out, abs)
		}
	}
	return out
}

func rm(w *judge.World, argv []string) *core.Effect {
	flags, long, paths := operands(argv)
	recursive := strings.ContainsAny(flags, "rR") || has(long, "--recursive")
	return w.Delete(quote(argv), existing(paths), recursive)
}

// chmeta models chown, chgrp and chmod: the first operand is the owner or
// mode unless --reference supplies it.
func chmeta(w *judge.World, argv []string) *core.Effect {
	flags, long, rest := operands(argv)
	// `chmod -x file` and `chmod -rwx file`: the "flags" are the mode.
	if strings.HasSuffix(argv[0], "chmod") {
		flags, rest = "", nil
		for i := 1; i < len(argv); i++ {
			a := argv[i]
			if a == "-R" || a == "--recursive" {
				flags += "R"
			} else if strings.HasPrefix(a, "--") || (strings.HasPrefix(a, "-") && len(a) == 2 && strings.ContainsAny(a[1:], "cfvR")) {
				continue
			} else {
				rest = append(rest, a)
			}
		}
	}
	if !has(long, "--reference") && len(rest) > 0 {
		rest = rest[1:]
	}
	recursive := strings.Contains(flags, "R") || has(long, "--recursive")
	return w.Chmeta(quote(argv), existing(rest), recursive, !strings.HasSuffix(argv[0], "chmod"))
}

// find matters only for -delete. The same expression with -delete swapped
// for -print0 names exactly what would go, so the judge sees the real set.
func find(w *judge.World, argv []string) *core.Effect {
	probe := []string{}
	deletes := false
	for _, a := range argv[1:] {
		switch a {
		case "-delete":
			deletes = true
			probe = append(probe, "-print0")
		case "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprintf", "-fls", "-fprint0":
			return nil // running the expression twice would repeat its side effects
		default:
			probe = append(probe, a)
		}
	}
	real := plat.LookReal("find")
	if !deletes || real == "" {
		return nil
	}
	out, err := exec.Command(real, probe...).Output()
	if err != nil && len(out) == 0 {
		return nil
	}
	var paths []string
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) > 0 {
			paths = append(paths, plat.Abs(string(p)))
		}
	}
	// Enough to judge by; a find over a million files is not read in full.
	return w.Delete(quote(argv), paths[:min(len(paths), 2000)], false)
}
