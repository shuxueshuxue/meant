// Package core is the vocabulary every other package speaks: an Effect is
// something a command concretely did (or was about to do) to the world, said
// in a form an agent can compare with what it meant.
package core

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Exit statuses meant gives a command of its own accord.
const (
	ExitStopped = 125 // stopped at a held effect
	// ExitRelayStopped is how a command relayed over ssh says it was stopped,
	// so that the machine that sent it stops too.
	ExitRelayStopped = 113
)

type Level int

const (
	Note     Level = 1 // worth one line in the digest
	Surprise Level = 2 // needs an explicit ack before it stands
)

// Kinds are for people reading the journal; no code branches on them.
const (
	Delete   = "delete"
	Chmeta   = "chmeta"
	Signal   = "signal"
	Publish  = "publish"
	Guard    = "guard"
	Declared = "declared"
	Unseen   = "unseen"
	Loss     = "loss"
	Change   = "change"
	Leftover = "leftover"
)

type Effect struct {
	Kind   string   `json:"kind"`
	Level  Level    `json:"level"`
	Act    string   `json:"act"`              // the concrete action, argv as executed
	Facts  []string `json:"facts,omitempty"`  // what it does to the world
	Why    []string `json:"why,omitempty"`    // why that is probably not what was meant
	Key    string   `json:"key,omitempty"`    // stable identity; the ack code is its hash
	Held   bool     `json:"held,omitempty"`   // true: stopped before it happened
	Remote string   `json:"remote,omitempty"` // set when the stop happened on another host, which has already reported it
	Acked  bool     `json:"acked,omitempty"`  // true: went ahead under an ack
	Undo   *Undo    `json:"undo,omitempty"`   // how to take it back, when it already happened
}

// Undo names the workspace snapshots around a command and the paths to put back.
type Undo struct {
	Workspace string   `json:"workspace"`
	Before    string   `json:"before"`
	After     string   `json:"after"`
	Paths     []string `json:"paths"`
}

// Code is the read receipt: four hex digits an agent can only know by having
// read the digest that describes this exact effect.
func (e Effect) Code() string {
	sum := sha256.Sum256([]byte(e.Key))
	return hex.EncodeToString(sum[:])[:4]
}

// MakeKey builds an identity that survives a rerun of the same action against
// the same targets, and changes when the targets do.
func MakeKey(kind string, parts ...string) string {
	sorted := append([]string(nil), parts...)
	sort.Strings(sorted)
	return kind + "\x00" + strings.Join(sorted, "\x00")
}

func Count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	if strings.HasSuffix(noun, "y") {
		return fmt.Sprintf("%s %sies", Commas(n), noun[:len(noun)-1])
	}
	if strings.HasSuffix(noun, "s") {
		return fmt.Sprintf("%s %ses", Commas(n), noun)
	}
	return fmt.Sprintf("%s %ss", Commas(n), noun)
}

func Commas(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func Bytes(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

// List renders up to max items and folds the rest into a count.
func List(items []string, max int) string {
	if len(items) <= max {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:max], ", ") + fmt.Sprintf(", … (+%d)", len(items)-max)
}

// Clip shortens s to at most n bytes for display.
func Clip(s string, n int) string {
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

// Quote makes one POSIX shell word.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
