// meant: command safety for agent harnesses.
//
// One binary plays every part. Invoked under another program's name (rm,
// kill, git, ...) it is that program's stand-in at an effect point; invoked
// as `meant` it runs commands, talks to harnesses, and shows its records.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"meant/internal/core"
	"meant/internal/harness"
	"meant/internal/judge"
	"meant/internal/ledger"
	"meant/internal/observe"
	"meant/internal/plat"
	"meant/internal/run"
	"meant/internal/verbs"
)

var version = "0.1.0"

const usage = `meant %s: command safety for agent harnesses.
Agents aren't malicious. They're mistaken.

  meant install <claude-code|codex> [--user]   attach to a harness
  meant run -c '<command>'                     run one command as a transaction
  meant run -- <program> [args...]
  meant remote add [-- ssh options] <host>     put meant on a host, so "ssh <host> ..." is observed there too
  meant remote allow <host>                    let "ssh <host> ..." run there unobserved, without stopping
  meant remote list | remove <host>

  meant status       effects waiting for an ack
  meant log [-n N]   what recent commands actually did
  meant undo [N]     put the workspace back to before the Nth-last command that changed it
  meant doctor       check that every observer works here

Inside an agent command:
  ack <code>         "I meant that": accept it, or let a stopped effect through
  reject <code>      "I did not": take it back
`

func main() {
	name := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	if name == "meant-wrap" {
		// Claude Code's shell prefix must be a bare executable path.
		if len(os.Args) != 2 {
			fail("usage: meant-wrap '<command line>'  (set as CLAUDE_CODE_SHELL_PREFIX)")
		}
		os.Exit(harness.Wrap(os.Args[1]))
	}
	if name != "meant" {
		os.Exit(verbs.Shim(name, os.Args))
	}
	if len(os.Args) < 2 {
		fmt.Printf(usage, version)
		return
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "run":
		os.Exit(runCmd(args))
	case "hook":
		os.Exit(harness.Hook())
	case "remote":
		remote(args)
	case "install":
		fs := flag.NewFlagSet("install", flag.ExitOnError)
		user := fs.Bool("user", false, "install for every project of this user instead of the current one")
		target := ""
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			target, args = args[0], args[1:]
		}
		fs.Parse(args)
		scope := "project"
		if *user {
			scope = "user"
		}
		msg, err := harness.Install(target, scope)
		if err != nil {
			fail(err.Error())
		}
		fmt.Println(msg)
	case "status":
		status()
	case "log":
		fs := flag.NewFlagSet("log", flag.ExitOnError)
		n := fs.Int("n", 20, "how many commands")
		fs.Parse(args)
		logCmd(*n)
	case "undo":
		undo(args)
	case "doctor":
		doctor(len(args) > 0 && args[0] == "--reset")
	case "version", "--version", "-v":
		fmt.Println(version)
	default:
		fmt.Printf(usage, version)
		os.Exit(2)
	}
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "meant: "+msg)
	os.Exit(2)
}

func runCmd(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	o := run.Options{}
	fs.StringVar(&o.Session, "s", "", "session id (default: the harness's, else one per workspace)")
	fs.StringVar(&o.Workspace, "w", "", "workspace root (default: the enclosing repository)")
	fs.StringVar(&o.Program, "c", "", "command line for the shell")
	fs.StringVar(&o.Shell, "shell", "", "shell to run it with (default: $SHELL)")
	fs.BoolVar(&o.Relay, "relay", false, "this command was sent here over ssh by another meant")
	grants := fs.String("g", "", "ack codes carried over from the sending side")
	fs.Parse(args)
	if *grants != "" {
		o.Grants = strings.Split(*grants, ",")
	}
	if o.Program == "" {
		if fs.NArg() == 0 {
			fail("nothing to run: give -c '<command>' or -- <program> [args...]")
		}
		words := make([]string, fs.NArg())
		for i, a := range fs.Args() {
			words[i] = core.Quote(a)
		}
		o.Program = strings.Join(words, " ")
		o.Display = strings.Join(fs.Args(), " ")
	}
	return run.Run(o)
}

func status() {
	found := false
	ents, _ := os.ReadDir(filepath.Join(plat.StateRoot(), "sessions"))
	for _, ent := range ents {
		for _, e := range ledger.Open(ent.Name()).Pending() {
			found = true
			fmt.Printf("[%s] session %s\n", e.Code(), ent.Name())
			for _, f := range e.Facts {
				fmt.Printf("    %s\n", f)
			}
			for _, w := range e.Why {
				fmt.Printf("    ! %s\n", w)
			}
		}
	}
	if !found {
		fmt.Println("nothing is waiting for an ack")
	}
}

func logCmd(n int) {
	for _, e := range ledger.Journal(n) {
		cmd := strings.ReplaceAll(e.Command, "\n", "; ")
		fmt.Printf("%s  exit %-3d %s\n", e.Time.Format("01-02 15:04:05"), e.Exit, core.Clip(cmd, 100))
		if d := (core.Digest{Effects: e.Effects, Blind: e.Blind}); !d.Empty() {
			fmt.Print(indent(d.Render()))
		}
	}
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n    ") + "\n"
}

// undo puts the workspace back to how it was before a recent command. Files
// that have changed again since are left alone and listed.
func undo(args []string) {
	nth := 1
	if len(args) > 0 {
		fmt.Sscan(args[0], &nth)
	}
	ws := plat.Resolve(run.FindWorkspace())
	entries := ledger.Journal(500)
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Workspace != ws || e.Before == "" || e.Before == e.After {
			continue
		}
		if nth--; nth > 0 {
			continue
		}
		msg, err := ledger.Restore(&core.Undo{Workspace: e.Workspace, Before: e.Before, After: e.After})
		if err != nil {
			fail(err.Error())
		}
		fmt.Printf("undid `%s`: %s\n", e.Command, msg)
		return
	}
	fail("no recorded command changed files in " + ws)
}

// doctor exercises each observer for real and says plainly what works.
func doctor(reset bool) {
	ws := plat.Resolve(run.FindWorkspace())
	if reset {
		if s, err := observe.Open(ws); err == nil {
			os.RemoveAll(s.Dir())
		} else {
			ents, _ := filepath.Glob(filepath.Join(plat.StateRoot(), "ws", "*", "disabled"))
			for _, f := range ents {
				os.Remove(f)
			}
		}
		os.RemoveAll(filepath.Join(plat.StateRoot(), "shims"))
		fmt.Println("reset: shims and this workspace's snapshots will be rebuilt on the next command")
	}
	w := judge.NewWorld(ws, "doctor", "", os.Getpid())
	fmt.Printf("meant %s\n  state      %s\n  workspace  %s\n  home       %s\n", version, plat.StateRoot(), orNone(w.Workspace), w.Home)
	shims, err := run.EnsureShims(w.Declare)
	report("effect points", err, fmt.Sprintf("%d stand-ins in %s", len(verbs.Names(w.Declare)), shims))
	snaps, err := observe.Open(w.Workspace)
	if err == nil {
		_, err = snaps.Take()
	}
	report("file changes", err, "workspace snapshots work")
	procs := plat.Procs()
	me := procs[os.Getpid()]
	var perr error
	if len(procs) == 0 {
		perr = fmt.Errorf("cannot read the process table")
	}
	report("processes", perr, fmt.Sprintf("%d visible; this one is %q", len(procs), me.Cmd))
}

func report(what string, err error, ok string) {
	if err != nil {
		fmt.Printf("  %-14s NOT WORKING: %v\n", what, err)
		return
	}
	fmt.Printf("  %-14s ok: %s\n", what, ok)
}

func orNone(s string) string {
	if s == "" {
		return "(none: the current directory is not narrower than your home)"
	}
	return s
}

// remote manages the hosts that commands are followed to over ssh.
//
// add copies this binary (or the build for that host's platform, found next
// to it) to the host and records where. Nothing is ever installed on a host
// that was not named here.
func remote(args []string) {
	if len(args) == 0 {
		args = []string{"list"}
	}
	list := verbs.Remotes()
	switch args[0] {
	case "list":
		if len(list) == 0 {
			fmt.Println("no remote hosts; `meant remote add <host>` puts meant on one")
		}
		for _, r := range list {
			where := r.Path
			if where == "" {
				where = "allowed, not observed"
			}
			fmt.Printf("%s\t%s\n", r.Host, where)
		}
	case "allow":
		if len(args) != 2 {
			fail("usage: meant remote allow <host>")
		}
		host := verbs.HostOf(args[1])
		if err := verbs.SetRemote(verbs.Remote{Host: host}); err != nil {
			fail(err.Error())
		}
		fmt.Printf("commands sent to %s by ssh now run there unobserved, without stopping\n", host)
	case "remove":
		if len(args) != 2 {
			fail("usage: meant remote remove <host>")
		}
		host := verbs.HostOf(args[1])
		if err := verbs.DropRemote(host); err != nil {
			fail(err.Error())
		}
		fmt.Printf("%s is no longer known: commands sent there stop again (any binary on it was left in place)\n", host)
	case "add":
		fs := flag.NewFlagSet("remote add", flag.ExitOnError)
		path := fs.String("path", "", "where to put the binary on the host (default: ~/.local/bin/meant there)")
		state := fs.String("state", "", "state directory on the host (default: that host's own default)")
		fs.Parse(args[1:])
		if fs.NArg() == 0 {
			fail("usage: meant remote add [--path P] [--state S] [-- ssh options] <host>")
		}
		sshArgs := fs.Args()
		host := verbs.HostOf(sshArgs[len(sshArgs)-1])
		ssh := func(stdin *os.File, command string) (string, error) {
			cmd := exec.Command("ssh", append(append([]string{}, sshArgs...), command)...)
			cmd.Stdin, cmd.Stderr = stdin, os.Stderr
			out, err := cmd.Output()
			return strings.TrimSpace(string(out)), err
		}
		// One round trip to learn what the host is and where its home is.
		info, err := ssh(nil, `uname -sm; printf '%s\n' "$HOME"`)
		lines := strings.Split(info, "\n")
		if err != nil || len(lines) < 2 {
			fail("cannot reach " + host + " with: ssh " + strings.Join(sshArgs, " "))
		}
		binary, err := binaryFor(lines[0])
		if err != nil {
			fail(err.Error())
		}
		if *path == "" {
			*path = lines[1] + "/.local/bin/meant"
		}
		f, err := os.Open(binary)
		if err != nil {
			fail(err.Error())
		}
		defer f.Close()
		q := core.Quote
		tmp := *path + ".new"
		if _, err := ssh(f, "mkdir -p "+q(filepath.Dir(*path))+" && cat > "+q(tmp)+" && chmod 755 "+q(tmp)+" && mv -f "+q(tmp)+" "+q(*path)); err != nil {
			fail("could not copy meant to " + host + ":" + *path)
		}
		check := q(*path) + " version"
		if *state != "" {
			check = "MEANT_HOME=" + q(*state) + " " + check
		}
		v, err := ssh(nil, check)
		if err != nil {
			fail("meant was copied to " + host + " but does not run there")
		}
		if err := verbs.SetRemote(verbs.Remote{Host: host, Path: *path, State: *state}); err != nil {
			fail(err.Error())
		}
		fmt.Printf("meant %s is at %s:%s (%s). `ssh %s <command>` run by an agent is now observed on %s.\n", v, host, *path, lines[0], host, host)
	default:
		fail("usage: meant remote add|allow|list|remove")
	}
}

// binaryFor finds the meant build for a host from its `uname -sm`.
func binaryFor(uname string) (string, error) {
	f := strings.Fields(strings.ToLower(uname))
	if len(f) != 2 {
		return "", fmt.Errorf("unexpected uname from host: %q", uname)
	}
	arch := map[string]string{"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[f[1]]
	if arch == "" {
		return "", fmt.Errorf("no build of meant for %s", uname)
	}
	self, err := plat.Self()
	if err != nil {
		return "", err
	}
	if f[0] == runtime.GOOS && arch == runtime.GOARCH {
		return self, nil
	}
	name := "meant-" + f[0] + "-" + arch
	for _, dir := range []string{filepath.Dir(self), filepath.Join(filepath.Dir(self), "..", "dist")} {
		if p := filepath.Join(dir, name); isFile(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("the host is %s; put a build named %s next to %s", uname, name, self)
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
