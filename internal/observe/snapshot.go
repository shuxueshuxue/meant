// Package observe holds the observers that look at state rather than at
// calls: what is different in the workspace after a command, and which of
// its processes are still alive. They need no model of the command at all,
// so they cover everything the effect-point shims cannot name: scripts,
// redirections, interpreters, tools nobody has heard of.
package observe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"meant/internal/plat"
)

// Snapshots is a private git repository whose work tree is the workspace.
// It never touches the project's own .git; it borrows git's index so that
// taking a snapshot costs about as much as `git status`, and git's object
// store so that any snapshot can be put back.
type Snapshots struct {
	Workspace string
	dir       string
	git       string
	autocrlf  string // the project's setting, so both repositories hash a file to the same id
}

type Change struct {
	Status string // A added, M modified, D deleted, T type changed
	Path   string
	Old    string // blob ids; empty when absent on that side
	New    string
	Mode   string // mode of the old side, for restoring
}

const empty = "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391" // git's blob id for zero bytes

func Open(workspace string) (*Snapshots, error) {
	git := plat.LookReal("git")
	if git == "" {
		return nil, errors.New("git is not installed")
	}
	if workspace == "" {
		return nil, errors.New("no workspace narrower than the home directory")
	}
	sum := sha256.Sum256([]byte(workspace))
	s := &Snapshots{Workspace: workspace, git: git, autocrlf: "false",
		dir: filepath.Join(plat.StateRoot(), "ws", hex.EncodeToString(sum[:])[:16])}
	if why, err := os.ReadFile(filepath.Join(s.dir, "disabled")); err == nil {
		return nil, fmt.Errorf("snapshots are off for this workspace (%s); `meant doctor --reset` turns them back on", strings.TrimSpace(string(why)))
	}
	probe := exec.Command(git, "config", "core.autocrlf")
	probe.Dir, probe.Env = workspace, cleanEnv()
	if out, err := probe.Output(); err == nil && strings.TrimSpace(string(out)) != "" {
		s.autocrlf = strings.TrimSpace(string(out))
	}
	if _, err := os.Stat(filepath.Join(s.dir, "HEAD")); err != nil {
		if err := os.MkdirAll(s.dir, 0o755); err != nil {
			return nil, err
		}
		if _, err := s.run(30*time.Second, nil, "init", "-q"); err != nil {
			return nil, err
		}
		os.WriteFile(filepath.Join(s.dir, "workspace"), []byte(workspace+"\n"), 0o644)
	}
	return s, nil
}

func (s *Snapshots) run(timeout time.Duration, stdin []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	base := []string{
		"-c", "core.autocrlf=" + s.autocrlf, "-c", "core.quotepath=false", "-c", "core.untrackedCache=true",
		"-c", "gc.auto=0", "-c", "advice.addEmbeddedRepo=false", "-c", "core.fsync=none",
	}
	cmd := exec.CommandContext(ctx, s.git, append(base, args...)...)
	cmd.Dir = s.Workspace
	// The user's git config must not leak in: filters, hooks and signing
	// belong to their repository, not to this bookkeeping one.
	cmd.Env = append(cleanEnv(), "GIT_DIR="+s.dir, "GIT_WORK_TREE="+s.Workspace,
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_OPTIONAL_LOCKS=0")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("took longer than %s", timeout)
	}
	if err != nil {
		return nil, fmt.Errorf("%s", strings.TrimSpace(firstLine(stderr.String())+" "+err.Error()))
	}
	return out, nil
}

func cleanEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return env
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

// Take records the workspace as it is now and returns the snapshot's id.
// The budget keeps a pathological workspace from stalling every command;
// the first snapshot has to hash everything and gets more room.
func (s *Snapshots) Take() (string, error) {
	budget := 10 * time.Second
	if _, err := os.Stat(filepath.Join(s.dir, "index")); err != nil {
		budget = 60 * time.Second
	}
	// Harnesses run tool calls in parallel; they share this one index.
	defer plat.Lock(filepath.Join(s.dir, "taking"), 2*budget)()
	// --ignore-errors: one unreadable file or half-made nested repository
	// must not cost the snapshot of everything else.
	if _, err := s.run(budget, nil, "add", "-A", "--ignore-errors", "."); err != nil && strings.Contains(err.Error(), "took longer") {
		// Paying this on every command would be worse than not looking.
		os.WriteFile(filepath.Join(s.dir, "disabled"), []byte("a snapshot "+err.Error()+"\n"), 0o644)
		return "", fmt.Errorf("the workspace is too large to snapshot (%v); file changes are not observed", err)
	}
	out, err := s.run(budget, nil, "write-tree")
	return strings.TrimSpace(string(out)), err
}

// Tidy lets git drop snapshot objects that are more than a week old, at most
// once a day and without making the command wait for it.
func (s *Snapshots) Tidy() {
	stamp := filepath.Join(s.dir, "tidied")
	if fi, err := os.Stat(stamp); err == nil && time.Since(fi.ModTime()) < 24*time.Hour {
		return
	}
	os.WriteFile(stamp, nil, 0o644)
	cmd := exec.Command(s.git, "gc", "--quiet", "--prune=7.days.ago")
	cmd.Dir = s.Workspace
	cmd.Env = append(cleanEnv(), "GIT_DIR="+s.dir, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	if cmd.Start() == nil {
		go cmd.Wait()
	}
}

// Dir is where this workspace's snapshots are kept.
func (s *Snapshots) Dir() string { return s.dir }

// Diff lists what changed between two snapshots.
func (s *Snapshots) Diff(before, after string) ([]Change, error) {
	if before == after {
		return nil, nil
	}
	out, err := s.run(30*time.Second, nil, "diff-tree", "-r", "-z", "--no-renames", before, after)
	if err != nil {
		return nil, err
	}
	var changes []Change
	f := strings.Split(string(out), "\x00")
	for i := 0; i+1 < len(f); i += 2 {
		meta := strings.Fields(strings.TrimPrefix(f[i], ":"))
		if len(meta) < 5 {
			continue
		}
		c := Change{Status: meta[4][:1], Path: f[i+1], Mode: meta[0]}
		if c.Status != "A" {
			c.Old = meta[2]
		}
		if c.Status != "D" {
			c.New = meta[3]
		}
		changes = append(changes, c)
	}
	return changes, nil
}

// ProjectHead is the commit the project's own repository is on, or "".
func (s *Snapshots) ProjectHead() string {
	out, err := s.project(nil, "rev-parse", "--verify", "-q", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (s *Snapshots) project(stdin []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.git, args...)
	cmd.Dir, cmd.Env = s.Workspace, cleanEnv()
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	return cmd.Output()
}

// Lost picks out the changes that destroyed work existing nowhere else.
//
// A change is a candidate when the old content was replaced by nothing, by
// an empty file, or by content the project's git already had (a revert):
// an ordinary edit produces content git has never seen and is not a loss.
// A candidate is then safe if git can still reach the old content: it was
// committed before the command (headBefore), or is committed, staged or
// stashed now. What remains was only ever in the working tree and is gone.
func (s *Snapshots) Lost(changes []Change, headBefore string) []Change {
	var cands []Change
	var ask bytes.Buffer
	places := []string{headBefore + ":", "HEAD:", ":", "refs/stash:", "refs/stash^3:"}
	for _, c := range changes {
		if c.Old == "" || c.Old == empty {
			continue
		}
		cands = append(cands, c)
		fmt.Fprintf(&ask, "%s\n%s\n", c.Old, orEmpty(c.New))
		for _, pl := range places {
			fmt.Fprintf(&ask, "%s%s\n", pl, c.Path)
		}
	}
	if len(cands) == 0 {
		return nil
	}
	out, err := s.project(ask.Bytes(), "cat-file", "--batch-check")
	lines := strings.Split(string(out), "\n")
	per := 2 + len(places)
	inRepo := err == nil && len(lines) >= len(cands)*per
	var lost []Change
	checked := 0
	for i, c := range cands {
		id := func(k int) string { // object id on answer line k, "" when missing
			if !inRepo {
				return ""
			}
			f := strings.Fields(lines[i*per+k])
			if len(f) == 3 && f[1] == "blob" {
				return f[0]
			}
			return ""
		}
		oldKnown, newKnown := id(0) != "", id(1) != ""
		if !(c.New == "" || c.New == empty || newKnown) {
			continue // an edit
		}
		safe := false
		for k := range places {
			safe = safe || id(2+k) == c.Old
		}
		// Known to git but not at any usual place: committed on another
		// branch, or dangling after a dropped stash. Ask git which.
		if !safe && oldKnown && checked < 16 {
			checked++
			found, _ := s.project(nil, "log", "--all", "-n", "1", "--format=%H", "--find-object="+c.Old)
			safe = len(bytes.TrimSpace(found)) > 0
		}
		if !safe {
			lost = append(lost, c)
		}
	}
	return lost
}

func orEmpty(id string) string {
	if id == "" {
		return empty
	}
	return id
}

// Restore puts paths back to their content in the before snapshot. A path
// is left alone, and reported, if it no longer looks the way the command
// left it: someone has worked on it since, and their work wins.
func (s *Snapshots) Restore(before, after string, paths []string) (restored, skipped []string, err error) {
	changes, err := s.Diff(before, after)
	if err != nil {
		return nil, nil, err
	}
	want := map[string]bool{}
	for _, p := range paths {
		want[p] = true
	}
	for _, c := range changes {
		if len(paths) > 0 && !want[c.Path] {
			continue
		}
		full := filepath.Join(s.Workspace, filepath.FromSlash(c.Path))
		if now := s.hash(full); now != c.New {
			skipped = append(skipped, c.Path)
			continue
		}
		if c.Old == "" {
			if err := os.Remove(full); err != nil {
				return restored, skipped, err
			}
		} else if err := s.checkout(c, full); err != nil {
			return restored, skipped, err
		}
		restored = append(restored, c.Path)
	}
	sort.Strings(restored)
	return restored, skipped, nil
}

// hash returns the blob id of a file as it is on disk, or "" if absent.
func (s *Snapshots) hash(full string) string {
	fi, err := os.Lstat(full)
	if err != nil {
		return ""
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(full)
		out, _ := s.run(10*time.Second, []byte(target), "hash-object", "--stdin")
		return strings.TrimSpace(string(out))
	}
	out, err := s.run(30*time.Second, nil, "hash-object", "--", full) // through the same filters (autocrlf) as the snapshot
	if err != nil {
		return "?"
	}
	return strings.TrimSpace(string(out))
}

func (s *Snapshots) checkout(c Change, full string) error {
	data, err := s.run(60*time.Second, nil, "cat-file", "blob", c.Old)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	os.Remove(full)
	if c.Mode == "120000" {
		return os.Symlink(string(data), full)
	}
	mode := os.FileMode(0o644)
	if c.Mode == "100755" {
		mode = 0o755
	}
	return os.WriteFile(full, data, mode)
}
