// Package verbs is the effect-point observer. A handful of programs change
// the world in ways that cannot be taken back; meant stands in for them on
// PATH. By the time one is executed, the shell has already expanded every
// variable, glob, substitution and trap, in whatever script or subprocess the
// call was buried in, so the shim sees the real argv: the action as it will
// happen, not as it was written.
//
// A verb only resolves what the invocation concretely does and hands that to
// the judge. Ordinary invocations are replaced by the real program at once.
package verbs

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"meant/internal/core"
	"meant/internal/judge"
	"meant/internal/ledger"
	"meant/internal/plat"
)

// A verb resolves one invocation into the effect it is about to have.
// nil means nothing worth saying.
type verb func(w *judge.World, argv []string) *core.Effect

var modeled = map[string]verb{
	"rm":      rm,
	"chown":   chmeta,
	"chgrp":   chmeta,
	"chmod":   chmeta,
	"find":    find,
	"kill":    kill,
	"pkill":   pkill,
	"killall": pkill,
	"git":     git,
}

// Names lists every program that gets a shim.
func Names(decls []judge.Declaration) []string {
	// `meant` itself is among them so that `meant undo` and `meant status`
	// work inside agent commands wherever the binary was installed.
	names := []string{"meant", "sudo", "ack", "reject"}
	for n := range modeled {
		names = append(names, n)
	}
	for _, n := range judge.DeclaredCommands(decls) {
		if _, dup := modeled[n]; !dup {
			names = append(names, n)
		}
	}
	return names
}

// Shim is the whole life of a stand-in process.
func Shim(name string, argv []string) int {
	real := plat.LookReal(name)
	// git runs hundreds of times inside other tools; all but a few of its
	// subcommands are none of meant's business and leave here at once.
	if name == "git" && real != "" && !gitMatters(argv) {
		plat.Exec(real, argv)
	}
	w := judge.FromEnv()
	switch name {
	case "ack", "reject":
		return resolve(w, name, argv, real)
	case "sudo":
		argv = sudo(w, argv)
	case "ssh":
		if w != nil {
			if status, handled := follow(w, argv, real); handled {
				return status
			}
		}
	}
	if w != nil {
		if e := resolveEffect(w, name, argv); e != nil && !proceed(w, e) {
			return core.ExitStopped
		}
	}
	if real == "" {
		fmt.Fprintf(os.Stderr, "%s: command not found\n", name)
		return 127
	}
	if err := plat.Exec(real, argv); err != nil {
		fmt.Fprintf(os.Stderr, "meant: cannot run %s: %v\n", real, err)
	}
	return 126
}

// resolveEffect never lets a bug in a verb break the command it models: a
// panic is reported in the digest and the real program runs.
func resolveEffect(w *judge.World, name string, argv []string) (e *core.Effect) {
	defer func() {
		if r := recover(); r != nil {
			e = nil
			ledger.Record(w.CmdDir, core.Effect{Kind: core.Unseen, Level: core.Note,
				Facts: []string{fmt.Sprintf("meant could not model `%s` (%v); it ran unobserved", strings.Join(argv, " "), r)}})
		}
	}()
	if v, ok := modeled[name]; ok {
		if e = v(w, argv); e != nil {
			return e
		}
	}
	return w.Declared(argv)
}

// proceed records the effect and says whether the real program may run. A
// surprise goes ahead only under an ack for exactly this effect; otherwise
// the shim reports it and waits to be torn down with the rest of the command.
func proceed(w *judge.World, e *core.Effect) bool {
	if e.Level >= core.Surprise {
		e.Acked = slices.Contains(ledger.Grants(w.CmdDir), e.Code())
		e.Held = !e.Acked
	}
	ledger.Record(w.CmdDir, *e)
	if e.Held {
		waitForTeardown(w)
	}
	return !e.Held
}

// waitForTeardown is where a shim that has recorded a held effect stays.
// The runner is watching the record file and will tear the command down,
// this process included. Until then control must not return to the shell:
// its next statement was written assuming this one succeeded. A shim the
// runner cannot signal (it was elevated by sudo) leaves on its own, a
// moment after the runner confirms the rest is already gone.
func waitForTeardown(w *judge.World) {
	for i := 0; i < 500; i++ {
		time.Sleep(10 * time.Millisecond)
		if ledger.Stopped(w.CmdDir) {
			time.Sleep(500 * time.Millisecond)
			break
		}
	}
}

// sudo keeps the observation alive across privilege elevation: sudo resets
// PATH, so the inner program is pointed back at its shim by absolute path and
// the markers are passed through explicitly.
func sudo(w *judge.World, argv []string) []string {
	shims := os.Getenv(plat.EnvShims)
	if shims == "" || w == nil {
		return argv
	}
	i := 1
	for i < len(argv) {
		a := argv[i]
		if a == "--" {
			i++
			break
		}
		if !strings.HasPrefix(a, "-") {
			break
		}
		// Options that take a separate value.
		if len(a) == 2 && strings.ContainsAny(a[1:], "ugphCDRTUrt") {
			i++
		}
		i++
	}
	if i >= len(argv) || !slices.Contains(Names(w.Declare), filepath.Base(argv[i])) {
		return argv
	}
	out := append([]string{}, argv[:i]...)
	out = append(out, "env")
	for _, k := range []string{plat.EnvSession, plat.EnvCmd, plat.EnvRoot, plat.EnvWS, plat.EnvShims} {
		out = append(out, k+"="+os.Getenv(k))
	}
	out = append(out, "PATH="+shims+string(os.PathListSeparator)+os.Getenv("PATH"))
	out = append(out, filepath.Join(shims, plat.ShimName(filepath.Base(argv[i]))))
	return append(out, argv[i+1:]...)
}

func quote(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if i == 0 {
			a = filepath.Base(a) // the program as the agent named it, not where its stand-in lives
		}
		if a == "" || strings.ContainsAny(a, " \t\n'\"\\$`*?[]{}()<>|&;~#!") {
			a = core.Quote(a)
		}
		parts[i] = a
	}
	return strings.Join(parts, " ")
}
