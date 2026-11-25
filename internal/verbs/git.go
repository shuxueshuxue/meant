package verbs

import (
	"fmt"
	"os/exec"
	"strings"

	"meant/internal/core"
	"meant/internal/judge"
	"meant/internal/plat"
)

// git is on PATH for everything, so the common case must cost nothing:
// find the subcommand and leave unless it is one of the few whose effect
// cannot be put back from here. Working-tree losses (reset --hard, checkout
// --, clean, stash drop) are deliberately absent: the snapshot observer sees
// their net result and can restore it, whichever command caused it.
func git(w *judge.World, argv []string) *core.Effect {
	global, sub, args := gitSplit(argv)
	real := plat.LookReal("git")
	if real == "" {
		return nil
	}
	run := func(extra ...string) (string, error) {
		out, err := exec.Command(real, append(append([]string{}, global...), extra...)...).CombinedOutput()
		return string(out), err
	}
	switch sub {
	case "push":
		return push(argv, args, run)
	case "commit":
		return markers(argv, run, contains(args, "-a", "--all", "-am"))
	default:
		return markers(argv, run, false)
	}
}

// gitSplit separates git's own options from the subcommand and its arguments.
func gitSplit(argv []string) (global []string, sub string, args []string) {
	i := 1
	for i < len(argv) && strings.HasPrefix(argv[i], "-") {
		if a := argv[i]; a == "-C" || a == "-c" || a == "--git-dir" || a == "--work-tree" || a == "--namespace" || a == "--exec-path" {
			i++
		}
		i++
	}
	if i >= len(argv) {
		return argv[1:], "", nil
	}
	return argv[1:i], argv[i], argv[i+1:]
}

// gitMatters is the cheap test made before anything else is loaded.
func gitMatters(argv []string) bool {
	_, sub, args := gitSplit(argv)
	switch sub {
	case "push", "commit":
		return true
	case "rebase", "merge", "cherry-pick", "revert", "am":
		return contains(args, "--continue")
	}
	return false
}

func contains(args []string, names ...string) bool {
	for _, a := range args {
		for _, n := range names {
			if a == n {
				return true
			}
		}
	}
	return false
}

// push asks git itself what the push would do, with the real remote and the
// real refs, and reports the updates that throw remote commits away.
func push(argv, args []string, run func(...string) (string, error)) *core.Effect {
	forced := false
	for _, a := range args {
		if a == "-f" || a == "--force" || a == "--mirror" || strings.HasPrefix(a, "--force-with-lease") ||
			strings.HasPrefix(a, "--force-if-includes") || (strings.HasPrefix(a, "+") && len(a) > 1) {
			forced = true
		}
	}
	if !forced {
		return nil // an unforced push can add to a remote but never discard from it
	}
	probe := []string{"push", "--dry-run", "--porcelain"}
	for _, a := range args {
		if a != "-q" && a != "--quiet" { // quiet would silence the very answer being asked for
			probe = append(probe, a)
		}
	}
	out, _ := run(probe...)
	e := &core.Effect{Kind: core.Publish, Act: quote(argv), Level: core.Surprise}
	var tags []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 3 || f[0] != "+" {
			continue
		}
		ref := f[1]
		if j := strings.Index(ref, ":"); j >= 0 {
			ref = ref[j+1:]
		}
		ref = strings.TrimPrefix(ref, "refs/heads/")
		old, new, _ := strings.Cut(strings.Fields(f[2])[0], "...")
		lost := "commits there that this clone has never fetched"
		if n, err := run("rev-list", "--count", new+".."+old); err == nil {
			lost = core.Count(atoi(n), "commit") + " that are there now"
		}
		e.Facts = append(e.Facts, fmt.Sprintf("would overwrite %s on the remote, discarding %s", ref, lost))
		tags = append(tags, ref, old)
	}
	if len(e.Facts) == 0 {
		return nil // forced in name only: nothing on the remote would be lost
	}
	e.Why = []string{"the remote has history this push does not contain; forcing replaces it for everyone"}
	e.Key = core.MakeKey(core.Publish, tags...)
	return e
}

// markers is a guard: a commit must not record files that still carry
// conflict markers. git can already tell; it just does not stop.
func markers(argv []string, run func(...string) (string, error), all bool) *core.Effect {
	out, _ := run("diff", "--cached", "--check")
	if all {
		more, _ := run("diff", "--check")
		out += more
	}
	seen := map[string]bool{}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "leftover conflict marker") {
			continue
		}
		if name, _, ok := strings.Cut(line, ":"); ok && !seen[name] {
			seen[name] = true
			files = append(files, name)
		}
	}
	if len(files) == 0 {
		return nil
	}
	return &core.Effect{
		Kind: core.Guard, Act: quote(argv), Level: core.Surprise,
		Facts: []string{fmt.Sprintf("would commit conflict markers in %s: %s", core.Count(len(files), "file"), core.List(files, 8))},
		Why:   []string{"`git add` marks a conflict as resolved without looking inside the file"},
		Key:   core.MakeKey(core.Guard, files...),
	}
}

func atoi(s string) int {
	n := 0
	fmt.Sscan(strings.TrimSpace(s), &n)
	return n
}
