// Package run is the transaction around one agent command:
//
//	before  snapshot the workspace
//	during  the command runs untouched, with meant's shims first on PATH;
//	        a shim that meets a surprising, irreversible effect records it
//	        and the whole command is stopped there
//	after   observers report what changed; the judge decides what deserves
//	        attention; the digest is appended to the command's output
//
// The command's own stdin, stdout, stderr and exit status pass straight
// through. An ordinary command is not altered and mostly not commented on.
package run

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"meant/internal/core"
	"meant/internal/judge"
	"meant/internal/ledger"
	"meant/internal/observe"
	"meant/internal/plat"
	"meant/internal/verbs"
)

type Options struct {
	Session   string
	Workspace string
	Program   string // what the shell is given to run
	Display   string // the agent's command, for the journal; defaults to Program
	Shell     string
	Relay     bool     // this command arrived over ssh from another meant
	Grants    []string // acks given on the sending side, valid for this command
}

func Run(o Options) int {
	if o.Workspace == "" {
		o.Workspace = FindWorkspace()
	}
	if o.Display == "" {
		o.Display = o.Program
	}
	sess := ledger.Open(ledger.SessionID(o.Session, o.Workspace))
	cmdDir := sess.NewCommand()
	world := judge.NewWorld(o.Workspace, sess.ID, cmdDir, os.Getpid())
	world.Save()
	if len(o.Grants) > 0 {
		ledger.Grant(cmdDir, o.Grants...)
	}
	var blind []string

	shims, err := EnsureShims(world.Declare)
	if err != nil {
		blind = append(blind, "effect points (could not set up shims: "+err.Error()+")")
	}

	snaps, err := observe.Open(world.Workspace)
	before, head := "", ""
	if err == nil {
		head = snaps.ProjectHead()
		before, err = snaps.Take()
	}
	// A command relayed over ssh usually starts in the home directory and
	// has no workspace; that is the normal case there, not news.
	if err != nil && !(o.Relay && world.Workspace == "") {
		blind = append(blind, "file changes ("+err.Error()+")")
	}

	shell, args := shellFor(o.Shell, o.Program)
	cmd := exec.Command(shell, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if o.Relay {
		cmd.Args[0] = filepath.Base(shell) // sshd starts the shell under its short name; $0 should not change
	}
	cmd.Env = append(os.Environ(),
		plat.EnvSession+"="+sess.ID,
		plat.EnvCmd+"="+cmdDir,
		plat.EnvRoot+"="+strconv.Itoa(os.Getpid()),
		plat.EnvWS+"="+o.Workspace,
		plat.EnvShims+"="+shims,
		"PATH="+shims+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	own := ownGroup(cmd)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "meant: cannot start %s: %v\n", shell, err)
		return 127
	}
	group := 0
	if own {
		group = cmd.Process.Pid
		plat.JoinGroup(group)
		judge.KeepGroups(sess.ID, group, nil)
	}
	exit, stopped := supervise(cmd, cmdDir, group)

	effects := ledger.Effects(cmdDir)
	after := ""
	if before != "" {
		if after, err = snaps.Take(); err != nil {
			blind = append(blind, "file changes ("+err.Error()+")")
		} else {
			effects = append(effects, fileEffects(world, snaps, head, before, after, cmdDir)...)
		}
	}
	procs := plat.Procs()
	if !stopped {
		if e := observe.LeftoverEffect(observe.Leftovers(procs, cmdDir, group)); e != nil {
			effects = append(effects, *e)
		}
	}
	judge.KeepGroups(sess.ID, group, procs)
	if group != 0 {
		plat.Outlive(group, procs)
	}
	effects = fold(effects)

	digest := core.Digest{Effects: effects, Pending: sess.Pending(), Blind: once(sess, blind)}
	if o.Relay {
		digest.Host, _ = os.Hostname()
	}
	for _, e := range effects {
		if e.Level == core.Surprise && !e.Held && !e.Acked {
			sess.Put(e)
		}
	}
	if stopped {
		exit = core.ExitStopped
		if o.Relay {
			exit = core.ExitRelayStopped // tells the sending side to stop as well
		}
	}
	ledger.Log(ledger.Entry{Time: time.Now(), Session: sess.ID, Command: o.Display, Exit: exit,
		Effects: effects, Blind: blind, Workspace: world.Workspace, Before: before, After: after})
	if !digest.Empty() {
		fmt.Fprint(os.Stderr, digest.Render())
	}
	sess.Sweep()
	if snaps != nil {
		snaps.Tidy()
	}
	return exit
}

// supervise waits for the command while watching what its shims record. The
// moment one reports a held effect, everything the command started is torn
// down: the rest of it was written on a premise that just turned out false.
func supervise(cmd *exec.Cmd, cmdDir string, group int) (exit int, stopped bool) {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, forwarded...)
	tick := time.NewTicker(15 * time.Millisecond)
	defer tick.Stop()
	log := filepath.Join(cmdDir, "effects.jsonl")
	var seen int64
	for {
		select {
		case err := <-done:
			if ee, ok := err.(*exec.ExitError); ok {
				return ee.ExitCode(), stopped
			} else if err != nil {
				return 126, stopped
			}
			return 0, stopped
		case <-sigs:
			teardown(cmd, cmdDir, group)
		case <-tick.C:
			if stopped {
				continue
			}
			fi, err := os.Stat(log)
			if err != nil || fi.Size() == seen {
				continue
			}
			seen = fi.Size()
			for _, e := range ledger.Effects(cmdDir) {
				if e.Held {
					stopped = true
					teardown(cmd, cmdDir, group)
					ledger.MarkStopped(cmdDir)
					break
				}
			}
		}
	}
}

// teardown stops the command's shell and every process carrying its marker,
// repeating until none is left: a process may be mid-fork on the first pass.
func teardown(cmd *exec.Cmd, cmdDir string, group int) {
	for pass := 0; pass < 5; pass++ {
		plat.Kill(cmd.Process.Pid, pass > 1)
		plat.KillGroup(group, pass > 1)
		left := observe.Leftovers(plat.Procs(), cmdDir, group)
		if len(left) == 0 && pass > 0 {
			return
		}
		for _, p := range left {
			plat.Kill(p.PID, pass > 1)
		}
		time.Sleep(time.Duration(20*(pass+1)) * time.Millisecond)
	}
}

// fileEffects turns the snapshot difference into at most two effects: a
// one-line account of ordinary changes, and a loss if uncommitted work was
// destroyed.
func fileEffects(w *judge.World, snaps *observe.Snapshots, head, before, after, cmdDir string) []core.Effect {
	changes, err := snaps.Diff(before, after)
	if err != nil || len(changes) == 0 {
		return nil
	}
	var out []core.Effect
	lost := snaps.Lost(changes, head)
	gone := map[string]bool{}
	var paths []string
	for _, c := range lost {
		gone[c.Path] = true
		paths = append(paths, c.Path)
	}
	if len(lost) > 0 {
		e := core.Effect{Kind: core.Loss, Level: core.Note,
			Undo: &core.Undo{Workspace: snaps.Workspace, Before: before, After: after, Paths: paths}}
		what := fmt.Sprintf("%s lost content that was never committed: %s", core.Count(len(lost), "file"), core.List(paths, 8))
		if len(lost) >= w.Limits.LostFiles {
			e.Level = core.Surprise
			e.Facts = []string{what}
			e.Why = []string{"that content existed only in the working tree; it is in no commit, stash or branch"}
			e.Key = core.MakeKey(core.Loss, append(paths, cmdDir)...)
		} else {
			e.Facts = []string{what + " (`meant undo` brings it back)"}
		}
		out = append(out, e)
	}
	var counts = map[string]int{}
	var names []string
	for _, c := range changes {
		if gone[c.Path] {
			continue
		}
		counts[c.Status]++
		names = append(names, map[string]string{"A": "+", "D": "-"}[c.Status]+c.Path)
	}
	if len(names) > 0 {
		var parts []string
		for _, s := range []struct{ k, sym string }{{"M", "~"}, {"T", "~"}, {"A", "+"}, {"D", "-"}} {
			if counts[s.k] > 0 {
				parts = append(parts, fmt.Sprintf("%s%d", s.sym, counts[s.k]))
			}
		}
		out = append(out, core.Effect{Kind: core.Change, Level: core.Note,
			Facts: []string{fmt.Sprintf("files %s (%s)", strings.Join(parts, " "), summarize(names))}})
	}
	return out
}

// summarize names a few paths, or the directories they fall in when there
// are too many to name.
func summarize(names []string) string {
	if len(names) <= 6 {
		return strings.Join(names, ", ")
	}
	dirs := map[string]int{}
	var order []string
	for _, n := range names {
		d := strings.TrimLeft(n, "+-")
		if i := strings.Index(d, "/"); i >= 0 {
			d = d[:i] + "/"
		} else {
			d = "./"
		}
		if dirs[d] == 0 {
			order = append(order, d)
		}
		dirs[d]++
	}
	var parts []string
	for _, d := range order {
		parts = append(parts, fmt.Sprintf("%s ×%d", d, dirs[d]))
	}
	return core.List(parts, 6)
}

// fold keeps a busy command from burying the agent: every surprise is kept,
// identical notes collapse, and the tail of the notes becomes a count.
func fold(effects []core.Effect) []core.Effect {
	var out []core.Effect
	seen := map[string]bool{}
	notes, dropped := 0, 0
	for _, e := range effects {
		if e.Level >= core.Surprise && !e.Acked {
			out = append(out, e)
			continue
		}
		id := e.Kind + "\x00" + strings.Join(e.Facts, "\x00") + e.Act
		if seen[id] {
			continue
		}
		seen[id] = true
		if notes++; notes > 5 {
			dropped++
			continue
		}
		out = append(out, e)
	}
	if dropped > 0 {
		out = append(out, core.Effect{Kind: core.Unseen, Level: core.Note,
			Facts: []string{fmt.Sprintf("%d more notes in `meant log`", dropped)}})
	}
	return out
}

// once drops blind-spot notices this session has already been told.
func once(sess *ledger.Session, blind []string) []string {
	var out []string
	for _, b := range blind {
		sum := sha256.Sum256([]byte(b))
		mark := filepath.Join(sess.Dir, "told-"+hex.EncodeToString(sum[:])[:10])
		if _, err := os.Stat(mark); err != nil {
			os.WriteFile(mark, []byte(b+"\n"), 0o644)
			out = append(out, b)
		}
	}
	return out
}

// FindWorkspace is the project the command runs in: what the harness says,
// else the enclosing repository, else the current directory.
func FindWorkspace() string {
	for _, k := range []string{plat.EnvWS, "CLAUDE_PROJECT_DIR"} {
		if d := os.Getenv(k); d != "" {
			return d
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	for d := cwd; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			return cwd
		}
	}
}

// EnsureShims makes sure the shim directory holds one alias of this binary
// per stand-in name, and returns it.
func EnsureShims(decls []judge.Declaration) (string, error) {
	self, err := plat.Self()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(plat.StateRoot(), "shims")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return dir, err
	}
	for _, name := range verbs.Names(decls) {
		link := filepath.Join(dir, plat.ShimName(name))
		if target, err := os.Readlink(link); err == nil {
			if target == self {
				continue
			}
			os.Remove(link) // points at a previous install
		} else if _, err := os.Lstat(link); err == nil {
			continue // a copy, where links are not available
		}
		if err := plat.Link(self, link); err != nil {
			return dir, err
		}
	}
	return dir, nil
}

// shellFor picks the shell the command is written for. For bash and zsh it
// adds two lines in front of the command:
//
//   - the `kill` builtin is routed through PATH so that it meets the shim too
//     (job specs like %1 stay with the builtin, the only thing that knows them);
//   - the shim directory is put back at the front of PATH whenever something
//     has moved it from there (Git Bash puts its own directories first). Harnesses source a profile snapshot before the command,
//     and commands source rc files and version managers, all of which rewrite
//     PATH. The guard hands $_ back untouched so `mkdir d && cd $_` still works.
func shellFor(shell, program string) (string, []string) {
	if shell == "" {
		shell = os.Getenv("SHELL")
	}
	switch strings.TrimSuffix(filepath.Base(shell), ".exe") {
	case "bash", "zsh":
		// Under Git Bash the shim directory arrives as C:\...; the shell wants /c/...
		pre := `__ms=$MEANT_SHIMS; case $__ms in [A-Za-z]:*) __ms=$(cygpath -u "$__ms");; esac; export __ms` + "\n" +
			`kill() { case "$*" in *%*) builtin kill "$@";; *) "$__ms/kill" "$@";; esac; }; [ -n "$BASH_VERSION" ] && export -f kill` + "\n" +
			`__meant_path() { case $PATH in "$__ms":*) ;; *) PATH="$__ms:$PATH";; esac; : "$1"; }; trap '__meant_path "$_"' DEBUG` + "\n"
		return shell, []string{"-c", pre + program}
	case "sh", "dash", "ksh", "fish":
		return shell, []string{"-c", program}
	}
	return defaultShell(program)
}
