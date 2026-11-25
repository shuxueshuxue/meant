// Package ledger is the memory between commands: which surprising effects
// have happened and not yet been acknowledged, and a journal of everything
// meant has said. It is plain files under the state root, one directory per
// agent session, so a crashed harness loses nothing and a human can read it.
package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"meant/internal/core"
	"meant/internal/observe"
	"meant/internal/plat"
)

type Session struct {
	ID  string
	Dir string
}

var unsafeID = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// SessionID picks the session a command belongs to: the harness's own id
// when it exposes one, otherwise one per workspace.
func SessionID(explicit, workspace string) string {
	for _, id := range []string{explicit, os.Getenv(plat.EnvSession), os.Getenv("CLAUDE_CODE_SESSION_ID"), os.Getenv("CODEX_THREAD_ID")} {
		if id != "" {
			return unsafeID.ReplaceAllString(id, "_")
		}
	}
	sum := sha256.Sum256([]byte(workspace))
	return "ws-" + hex.EncodeToString(sum[:])[:12]
}

func Open(id string) *Session {
	s := &Session{ID: id, Dir: filepath.Join(plat.StateRoot(), "sessions", id)}
	os.MkdirAll(s.Dir, 0o755)
	return s
}

// NewCommand makes the state directory one command's shims write into.
func (s *Session) NewCommand() string {
	dir := filepath.Join(s.Dir, "cmd", fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid()))
	os.MkdirAll(dir, 0o755)
	return dir
}

// A command directory is how one command's shims and its runner talk:
//
//	effects.jsonl  what the shims met, one effect per line
//	grants         codes acked for this command
//	stopped        the runner has torn the command down
//	world.json     the frame of reference (written by judge)

// Record appends one effect. Appends of a single short line are atomic, so
// parallel shims need no lock.
func Record(cmdDir string, e core.Effect) { appendJSON(filepath.Join(cmdDir, "effects.jsonl"), e) }

// Effects reads what the shims of a command recorded.
func Effects(cmdDir string) []core.Effect {
	raw, _ := os.ReadFile(filepath.Join(cmdDir, "effects.jsonl"))
	var out []core.Effect
	for _, line := range strings.Split(string(raw), "\n") {
		var e core.Effect
		if json.Unmarshal([]byte(line), &e) == nil && (e.Act != "" || len(e.Facts) > 0) {
			out = append(out, e)
		}
	}
	return out
}

// Grants lists the codes acked for a command.
func Grants(cmdDir string) []string {
	raw, _ := os.ReadFile(filepath.Join(cmdDir, "grants"))
	return strings.Fields(string(raw))
}

func Grant(cmdDir string, codes ...string) error {
	f, err := os.OpenFile(filepath.Join(cmdDir, "grants"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(strings.Join(codes, "\n") + "\n")
	return err
}

func MarkStopped(cmdDir string) { os.WriteFile(filepath.Join(cmdDir, "stopped"), nil, 0o644) }

func Stopped(cmdDir string) bool {
	_, err := os.Stat(filepath.Join(cmdDir, "stopped"))
	return err == nil
}

func (s *Session) pendingFile() string { return filepath.Join(s.Dir, "pending.json") }

// Pending lists the surprising effects that happened and are unresolved.
func (s *Session) Pending() []core.Effect {
	var list []core.Effect
	raw, err := os.ReadFile(s.pendingFile())
	if err == nil {
		json.Unmarshal(raw, &list)
	}
	return list
}

func (s *Session) write(list []core.Effect) {
	raw, _ := json.MarshalIndent(list, "", "  ")
	tmp := s.pendingFile() + ".tmp"
	if os.WriteFile(tmp, raw, 0o644) == nil {
		os.Rename(tmp, s.pendingFile())
	}
}

// Put adds an effect to the unresolved list.
func (s *Session) Put(e core.Effect) {
	defer s.lock()()
	s.write(append(s.Pending(), e))
}

// Take removes and returns the unresolved effect with this code.
func (s *Session) Take(code string) (core.Effect, bool) {
	defer s.lock()()
	list := s.Pending()
	for i, e := range list {
		if e.Code() == code {
			s.write(append(list[:i:i], list[i+1:]...))
			return e, true
		}
	}
	return core.Effect{}, false
}

func (s *Session) lock() func() { return plat.Lock(filepath.Join(s.Dir, "lock"), 5*time.Second) }

// Entry is one line of the journal: a command and what meant said about it.
type Entry struct {
	Time      time.Time     `json:"time"`
	Session   string        `json:"session"`
	Command   string        `json:"command"`
	Exit      int           `json:"exit"`
	Effects   []core.Effect `json:"effects,omitempty"`
	Blind     []string      `json:"blind,omitempty"`
	Workspace string        `json:"workspace,omitempty"`
	Before    string        `json:"before,omitempty"` // snapshot ids, for `meant undo`
	After     string        `json:"after,omitempty"`
}

func journalFile() string { return filepath.Join(plat.StateRoot(), "journal.jsonl") }

func Log(e Entry) { appendJSON(journalFile(), e) }

func appendJSON(file string, v any) {
	line, _ := json.Marshal(v)
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(line, '\n'))
}

// Journal returns the most recent entries, oldest first.
func Journal(max int) []Entry {
	raw, _ := os.ReadFile(journalFile())
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) > max {
		lines = lines[len(lines)-max:]
	}
	var out []Entry
	for _, l := range lines {
		var e Entry
		if json.Unmarshal([]byte(l), &e) == nil && !e.Time.IsZero() {
			out = append(out, e)
		}
	}
	return out
}

// Restore puts a workspace back using the snapshots around a command.
func Restore(u *core.Undo) (string, error) {
	if u == nil {
		return "", fmt.Errorf("this effect cannot be taken back from here")
	}
	snaps, err := observe.Open(u.Workspace)
	if err != nil {
		return "", err
	}
	restored, skipped, err := snaps.Restore(u.Before, u.After, u.Paths)
	if err != nil {
		return "", err
	}
	msg := fmt.Sprintf("restored %s (%s)", core.Count(len(restored), "file"), core.List(restored, 6))
	if len(skipped) > 0 {
		sort.Strings(skipped)
		msg += fmt.Sprintf("; left %s alone because they changed again since: %s", core.Count(len(skipped), "file"), core.List(skipped, 6))
	}
	return msg, nil
}

// Sweep removes command directories older than a day.
func (s *Session) Sweep() {
	ents, _ := os.ReadDir(filepath.Join(s.Dir, "cmd"))
	for _, e := range ents {
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > 24*time.Hour {
			os.RemoveAll(filepath.Join(s.Dir, "cmd", e.Name()))
		}
	}
}
