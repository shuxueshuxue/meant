//go:build linux

package plat

import (
	"bytes"
	"os"
	"strconv"
	"strings"
)

// Procs reads the process table from /proc. Markers come from each process's
// initial environment, readable for the invoking user's own processes (and
// for all of them as root); an unreadable environment means "not ours".
func Procs() map[int]Proc {
	out := map[int]Proc{}
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return out
	}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if p, ok := readProc(pid); ok {
			out[pid] = p
		}
	}
	return out
}

func readProc(pid int) (Proc, bool) {
	dir := "/proc/" + strconv.Itoa(pid)
	stat, err := os.ReadFile(dir + "/stat")
	if err != nil {
		return Proc{}, false
	}
	p := Proc{PID: pid, UID: -1}
	// Fields after the parenthesised name: state ppid pgrp ...
	if i := bytes.LastIndexByte(stat, ')'); i >= 0 {
		f := strings.Fields(string(stat[i+1:]))
		if len(f) >= 3 {
			p.PPID, _ = strconv.Atoi(f[1])
			p.PGID, _ = strconv.Atoi(f[2])
		}
	}
	if fi, err := os.Stat(dir); err == nil {
		p.UID = Facts(fi).UID
	}
	if raw, err := os.ReadFile(dir + "/cmdline"); err == nil && len(raw) > 0 {
		p.Cmd = strings.TrimSpace(strings.ReplaceAll(string(raw), "\x00", " "))
	} else if i, j := bytes.IndexByte(stat, '('), bytes.LastIndexByte(stat, ')'); i >= 0 && j > i {
		p.Cmd = "[" + string(stat[i+1:j]) + "]"
	}
	if env, err := os.ReadFile(dir + "/environ"); err == nil {
		for _, kv := range bytes.Split(env, []byte{0}) {
			if v, ok := bytes.CutPrefix(kv, []byte(EnvSession+"=")); ok {
				p.Session = string(v)
			} else if v, ok := bytes.CutPrefix(kv, []byte(EnvCmd+"=")); ok {
				p.CmdDir = string(v)
			}
		}
	}
	return p, true
}
