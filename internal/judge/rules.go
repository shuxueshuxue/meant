package judge

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"meant/internal/core"
	"meant/internal/plat"
)

// census is what a tree actually contains, counted the way the kernel will
// see it: no symlinks followed, every entry looked at once.
type census struct {
	Entries, Dirs int
	Bytes         int64
	Foreign       int            // entries owned by someone else
	Owners        map[string]int // who
	Hardlinked    int            // regular files reachable from other paths too
	Devices       []string
	Setuid        int
	Mounts        []string // other filesystems mounted inside the tree
	Capped        bool     // gave up counting; numbers are lower bounds
}

const (
	scanEntries = 300000
	scanTime    = 3 * time.Second
)

func (w *World) scan(root string, recursive bool) census {
	c := census{Owners: map[string]int{}}
	deadline := time.Now().Add(scanTime)
	var rootDev uint64
	look := func(path string, fi fs.FileInfo, isRoot bool) {
		f := plat.Facts(path, fi)
		c.Entries++
		if fi.IsDir() {
			c.Dirs++
			if isRoot {
				rootDev = f.Dev
			} else if f.Dev != rootDev {
				c.Mounts = append(c.Mounts, path)
			}
		} else if fi.Mode().IsRegular() {
			c.Bytes += fi.Size()
			if f.Nlink > 1 {
				c.Hardlinked++
			}
			if f.Setuid {
				c.Setuid++
			}
		}
		if f.Device {
			c.Devices = append(c.Devices, path)
		}
		if f.UID >= 0 && f.UID != w.UID {
			c.Foreign++
			c.Owners[plat.UserName(f.UID)]++
		}
	}
	fi, err := os.Lstat(root)
	if err != nil {
		return c
	}
	look(root, fi, true)
	if !recursive || !fi.IsDir() {
		return c
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == root {
			return nil
		}
		if c.Entries >= scanEntries || (c.Entries%512 == 0 && time.Now().After(deadline)) {
			c.Capped = true
			return filepath.SkipAll
		}
		if fi, err := d.Info(); err == nil {
			look(path, fi, false)
		}
		return nil
	})
	return c
}

func (c census) size() string {
	more := ""
	if c.Capped {
		more = "more than "
	}
	if c.Dirs == 0 {
		return more + core.Count(c.Entries, "file")
	}
	return fmt.Sprintf("%s%s in %s (%s)", more, core.Count(c.Entries-c.Dirs, "file"), core.Count(c.Dirs, "dir"), core.Bytes(c.Bytes))
}

func (c census) owners() string {
	var names []string
	for n := range c.Owners {
		names = append(names, n)
	}
	sort.Strings(names)
	return core.List(names, 4)
}

// anchors are the reasons a path matters regardless of what is in it.
func (w *World) anchors(path string, recursive bool) (why, tags []string) {
	add := func(tag, reason string) { why, tags = append(why, reason), append(tags, tag) }
	switch {
	case plat.IsFSRoot(path):
		add("fsroot", path+" is the root of a mounted filesystem")
	case path == w.Home:
		add("home", path+" is your real home directory (whatever $HOME says right now)")
	case recursive && plat.Within(w.Home, path):
		add("above-home", path+" contains your home directory")
	}
	if w.Workspace != "" && recursive {
		switch {
		case path == w.Workspace:
			add("workspace", path+" is the entire workspace")
		case plat.Within(w.Workspace, path):
			add("above-workspace", path+" contains the workspace")
		case path == filepath.Join(w.Workspace, ".git"):
			add("history", path+" is this repository's whole history")
		}
	}
	return
}

// Delete judges removing paths. Inside the workspace it stays silent: the
// snapshot observer sees the net result there and can put it back.
func (w *World) Delete(act string, paths []string, recursive bool) *core.Effect {
	e := &core.Effect{Kind: core.Delete, Act: act}
	f := fact{would: "would delete", did: "deleted"}
	var tags []string
	var far census // the part that lies outside the agent's own turf
	for _, p := range paths {
		why, t := w.anchors(p, recursive)
		where := w.turfOf(p)
		if len(why) == 0 && where == workspace {
			continue
		}
		c := w.scan(p, recursive)
		if c.Entries == 0 || (len(why) == 0 && where == scratch && c.Foreign == 0 && len(c.Mounts) == 0) {
			continue
		}
		if where == outside {
			far.add(c)
		}
		f.total.add(c)
		f.roots = append(f.roots, p)
		e.Why = append(e.Why, why...)
		tags = append(append(tags, p), t...)
	}
	if f.total.Foreign > 0 {
		e.Why = append(e.Why, fmt.Sprintf("%s of them belong to other users (%s)", core.Commas(f.total.Foreign), f.total.owners()))
	}
	if len(f.total.Mounts) > 0 {
		e.Why = append(e.Why, "it reaches into other filesystems mounted at "+core.List(f.total.Mounts, 3))
	}
	if far.Entries > w.Limits.OutsideEntries {
		e.Why = append(e.Why, "it is outside your workspace and temp directories")
	}
	return w.settle(e, f, tags)
}

// Chmeta judges changing owner or mode. Unlike a delete it cannot be put
// back from a snapshot, and its reach is defined by inodes, not paths.
func (w *World) Chmeta(act string, paths []string, recursive, chown bool) *core.Effect {
	what := map[bool]string{true: "ownership", false: "permissions"}[chown]
	e := &core.Effect{Kind: core.Chmeta, Act: act}
	f := fact{would: "would change " + what + " of", did: "changed " + what + " of"}
	var tags []string
	var far census
	for _, p := range paths {
		p = plat.Resolve(p) // chown and chmod follow the link they are given
		why, t := w.anchors(p, recursive)
		c := w.scan(p, recursive)
		if c.Entries == 0 {
			continue
		}
		where := w.turfOf(p)
		for _, sys := range plat.SystemDirs() {
			if plat.Within(p, sys) && len(c.Devices) == 0 {
				why = append(why, p+" is part of the operating system; other programs depend on its owner and mode")
				t = append(t, "system")
				break
			}
		}
		reach := len(c.Devices) > 0 || c.Hardlinked > 0 || len(c.Mounts) > 0 || (chown && c.Setuid > 0)
		if len(why) == 0 && where != outside && !reach {
			continue
		}
		if where == outside {
			far.add(c)
		}
		f.total.add(c)
		f.roots = append(f.roots, p)
		e.Why = append(e.Why, why...)
		tags = append(append(tags, p), t...)
	}
	t := f.total
	if len(t.Devices) > 0 {
		e.Why = append(e.Why, core.List(t.Devices, 3)+" is a device node that every process on this machine shares")
	}
	if t.Hardlinked > 0 {
		e.Why = append(e.Why, fmt.Sprintf("%s here are hard links: the same change lands on every other path to those files", core.Count(t.Hardlinked, "file")))
	}
	if len(t.Mounts) > 0 {
		e.Why = append(e.Why, "it reaches into other filesystems mounted at "+core.List(t.Mounts, 3))
	}
	if chown && t.Setuid > 0 {
		e.Why = append(e.Why, fmt.Sprintf("%s would lose the setuid bit", core.Count(t.Setuid, "setuid program")))
	}
	if far.Foreign > 0 {
		e.Why = append(e.Why, fmt.Sprintf("%s of them belong to other users (%s)", core.Commas(far.Foreign), far.owners()))
	}
	if far.Entries > w.Limits.OutsideEntries {
		e.Why = append(e.Why, "it is outside your workspace and temp directories")
	}
	return w.settle(e, f, tags)
}

// Signal judges sending a signal. A process is the agent's own when it
// carries this session's marker or descends from the running command.
func (w *World) Signal(act string, pids []int) *core.Effect {
	procs := plat.Procs()
	e := &core.Effect{Kind: core.Signal, Act: act}
	var tags, theirs []string
	targets := 0
	for _, pid := range pids {
		switch {
		case pid == -1:
			e.Why = append(e.Why, "-1 means every process this user is allowed to signal")
			tags = append(tags, "everyone")
			targets++
			continue
		case pid == 1:
			e.Why = append(e.Why, "pid 1 is init: signalling it can take down the machine or container")
			tags = append(tags, "init")
			targets++
			continue
		case pid == 0:
			continue
		}
		var group []plat.Proc
		if pid < 0 {
			for _, p := range procs {
				if p.PGID == -pid {
					group = append(group, p)
				}
			}
		} else if p, ok := procs[pid]; ok {
			group = []plat.Proc{p}
		}
		for _, p := range group {
			targets++
			if w.ours(procs, p) {
				continue
			}
			theirs = append(theirs, fmt.Sprintf("%d %s (%s)", p.PID, core.Clip(p.Cmd, 60), plat.UserName(p.UID)))
			tags = append(tags, fmt.Sprint(p.PID))
		}
	}
	if len(theirs) > 0 {
		sort.Strings(theirs)
		e.Why = append(e.Why, fmt.Sprintf("%d of %d targets were not started by this session: %s", len(theirs), targets, core.List(theirs, 6)))
	}
	if len(e.Why) == 0 {
		return nil
	}
	e.Facts = []string{"would signal " + core.Count(targets, "process")}
	e.Level, e.Key = core.Surprise, core.MakeKey(e.Kind, tags...)
	return e
}

func (w *World) ours(procs map[int]plat.Proc, p plat.Proc) bool {
	if w.Session != "" && p.Session == w.Session {
		return true
	}
	if w.RootPID > 1 && plat.Descends(procs, p.PID, w.RootPID) {
		return true
	}
	for _, g := range groups(w.Session) {
		if plat.InGroup(p, g) {
			return true
		}
	}
	return false
}

// settle turns what was found into a verdict: nothing to say, a note, or a
// surprise with a stable key. The fact is phrased for before or after the
// event accordingly: a surprise is described while it can still be stopped.
func (w *World) settle(e *core.Effect, f fact, tags []string) *core.Effect {
	if len(f.roots) == 0 {
		return nil
	}
	verb := f.did
	e.Level = core.Note
	if len(e.Why) > 0 {
		verb = f.would
		e.Level = core.Surprise
		e.Key = core.MakeKey(e.Kind, tags...)
		e.Why = unique(e.Why)
	}
	where := "under " + f.roots[0]
	if len(f.roots) > 1 {
		// Name the directory they share and a few of the entries in it.
		dir := filepath.Dir(f.roots[0])
		for _, r := range f.roots {
			for !plat.Within(r, dir) {
				dir = filepath.Dir(dir)
			}
		}
		var names []string
		for _, r := range f.roots {
			rel, _ := filepath.Rel(dir, r)
			names = append(names, rel)
		}
		where = fmt.Sprintf("under %s: %s", dir, core.List(names, 4))
	} else if f.total.Entries == 1 {
		where = "(" + f.roots[0] + ")"
	}
	e.Facts = []string{fmt.Sprintf("%s %s %s", verb, f.total.size(), where)}
	return e
}

type fact struct {
	would, did string
	total      census
	roots      []string
}

func (c *census) add(o census) {
	c.Entries += o.Entries
	c.Dirs += o.Dirs
	c.Bytes += o.Bytes
	c.Foreign += o.Foreign
	c.Hardlinked += o.Hardlinked
	c.Setuid += o.Setuid
	c.Capped = c.Capped || o.Capped
	c.Devices = append(c.Devices, o.Devices...)
	c.Mounts = append(c.Mounts, o.Mounts...)
	if c.Owners == nil {
		c.Owners = map[string]int{}
	}
	for k, v := range o.Owners {
		c.Owners[k] += v
	}
}

func unique(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(out) > 6 {
		out = append(out[:6], fmt.Sprintf("and %d more reasons like these", len(out)-6))
	}
	return out
}
