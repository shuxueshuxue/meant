package judge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meant/internal/core"
	"meant/internal/plat"
)

// world builds a frame of reference out of temp directories: a home, a
// workspace inside it, and a scratch zone, so the anchor rules can be
// exercised without going anywhere near the real ones.
func world(t *testing.T) (*World, string) {
	t.Helper()
	root := plat.Resolve(t.TempDir())
	home := filepath.Join(root, "home", "u")
	ws := filepath.Join(home, "proj")
	scratch := filepath.Join(root, "scratch")
	for _, d := range []string{ws, scratch, filepath.Join(home, "Documents")} {
		os.MkdirAll(d, 0o755)
	}
	return &World{Workspace: ws, Home: home, Zones: []string{scratch}, UID: os.Getuid(),
		Limits: Limits{OutsideEntries: 5, LostFiles: 3}}, root
}

func fill(t *testing.T, dir string, n int) {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	for i := 0; i < n; i++ {
		os.WriteFile(filepath.Join(dir, "f"+string(rune('a'+i))), []byte("x"), 0o644)
	}
}

func why(e *core.Effect) string {
	if e == nil {
		return ""
	}
	return strings.Join(e.Why, " | ")
}

func TestDelete(t *testing.T) {
	w, root := world(t)
	fill(t, filepath.Join(w.Workspace, "build"), 20)
	fill(t, filepath.Join(w.Home, "Documents"), 20)
	fill(t, filepath.Join(w.Home, "small"), 2)
	fill(t, filepath.Join(root, "scratch", "tmp1"), 20)

	cases := []struct {
		name      string
		path      string
		recursive bool
		level     core.Level // 0: nothing to say
		reason    string
	}{
		{"inside the workspace is the snapshot's business", filepath.Join(w.Workspace, "build"), true, 0, ""},
		{"own scratch is the agent's to churn", filepath.Join(root, "scratch", "tmp1"), true, 0, ""},
		{"a little outside is worth a line", filepath.Join(w.Home, "small"), true, core.Note, ""},
		{"a lot outside needs an ack", filepath.Join(w.Home, "Documents"), true, core.Surprise, "outside your workspace"},
		{"the real home, whatever $HOME says", w.Home, true, core.Surprise, "real home directory"},
		{"a parent of home", filepath.Dir(w.Home), true, core.Surprise, "contains your home"},
		{"the whole workspace", w.Workspace, true, core.Surprise, "entire workspace"},
		{"the repository's history", filepath.Join(w.Workspace, ".git"), true, core.Surprise, "whole history"},
	}
	os.MkdirAll(filepath.Join(w.Workspace, ".git"), 0o755)
	for _, c := range cases {
		e := w.Delete("rm", []string{c.path}, c.recursive)
		got := core.Level(0)
		if e != nil {
			got = e.Level
		}
		if got != c.level || !strings.Contains(why(e), c.reason) {
			t.Errorf("%s: level %d, why %q; want level %d containing %q", c.name, got, why(e), c.level, c.reason)
		}
	}
}

// Another user's files look the same as one's own to root. Seen from a
// different uid, the files this test just made are "someone else's".
func TestDeleteForeign(t *testing.T) {
	w, root := world(t)
	shared := filepath.Join(root, "scratch", "shared")
	fill(t, shared, 3)
	w.UID = os.Getuid() + 1
	e := w.Delete("rm", []string{shared}, true)
	if e == nil || e.Level != core.Surprise || !strings.Contains(why(e), "belong to other users") {
		t.Fatalf("deleting other users' files in a shared temp dir: %+v", e)
	}
}

func TestChmeta(t *testing.T) {
	w, root := world(t)
	tmpl := filepath.Join(root, "scratch", "template")
	slot := filepath.Join(root, "scratch", "slot")
	fill(t, tmpl, 3)
	os.MkdirAll(slot, 0o755)
	for _, n := range []string{"fa", "fb", "fc"} {
		if err := os.Link(filepath.Join(tmpl, n), filepath.Join(slot, n)); err != nil {
			t.Skip("no hard links here")
		}
	}
	plain := filepath.Join(w.Workspace, "script.sh")
	os.WriteFile(plain, []byte("#!/bin/sh\n"), 0o644)

	if e := w.Chmeta("chmod +x", []string{plain}, false, false); e != nil {
		t.Errorf("chmod +x on a workspace file should pass in silence: %+v", e)
	}
	if e := w.Chmeta("chown -R", []string{slot}, true, true); e == nil || !strings.Contains(why(e), "hard links") {
		t.Errorf("chown -R over hard links must say the change lands elsewhere too: %+v", e)
	}
	if _, err := os.Stat(os.DevNull); err == nil && os.DevNull == "/dev/null" {
		if e := w.Chmeta("chmod 644", []string{"/dev/null"}, false, false); e == nil || !strings.Contains(why(e), "device node") {
			t.Errorf("chmod on /dev/null: %+v", e)
		}
	}
}

func TestSignal(t *testing.T) {
	w, _ := world(t)
	w.RootPID = os.Getpid()
	if e := w.Signal("kill", []int{1}); e == nil || !strings.Contains(why(e), "init") {
		t.Errorf("pid 1: %+v", e)
	}
	if e := w.Signal("kill", []int{-1}); e == nil || !strings.Contains(why(e), "every process") {
		t.Errorf("-1: %+v", e)
	}
	if e := w.Signal("kill", []int{os.Getpid()}); e != nil {
		t.Errorf("a process of this command is the agent's own: %+v", e)
	}
	if e := w.Signal("kill", []int{os.Getppid()}); e == nil || !strings.Contains(why(e), "not started by this session") {
		t.Errorf("the parent of the command is not the agent's: %+v", e)
	}
}

func TestDeclared(t *testing.T) {
	w := &World{Declare: builtinDeclarations}
	hold := [][]string{
		{"drizzle-kit", "push", "--force"},
		{"terraform", "apply", "-auto-approve"},
		{"/usr/bin/kubectl", "-n", "prod", "delete", "pod", "x"},
		{"dd", "if=/dev/zero", "of=/dev/sda"},
	}
	for _, argv := range hold {
		if e := w.Declared(argv); e == nil || e.Level != core.Surprise {
			t.Errorf("%v should need an ack: %+v", argv, e)
		}
	}
	pass := [][]string{
		{"terraform", "plan"},
		{"kubectl", "get", "pods"},
		{"dd", "if=a", "of=b.img"},
		{"docker", "ps"},
		{"ssh", "-V"},
	}
	for _, argv := range pass {
		if e := w.Declared(argv); e != nil {
			t.Errorf("%v should pass in silence: %+v", argv, e)
		}
	}
	if e := w.Declared([]string{"ssh", "gpu", "rm -rf /tmp/x"}); e == nil || e.Level != core.Note {
		t.Errorf("a remote command is noted as unobserved: %+v", e)
	}
}

func TestCodeIsStableAcrossCounts(t *testing.T) {
	w, _ := world(t)
	dir := filepath.Join(w.Home, "Documents")
	fill(t, dir, 10)
	a := w.Delete("rm", []string{dir}, true)
	fill(t, filepath.Join(dir, "more"), 4)
	b := w.Delete("rm", []string{dir}, true)
	if a.Code() != b.Code() {
		t.Errorf("the same action on the same target must keep its code: %s vs %s", a.Code(), b.Code())
	}
	c := w.Delete("rm", []string{w.Home}, true)
	if c.Code() == a.Code() {
		t.Errorf("a different target must not share a code")
	}
}
