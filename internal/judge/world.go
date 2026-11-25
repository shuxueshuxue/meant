// Package judge is the one place that decides whether a concrete effect is
// something an agent is likely to have meant. Verbs and observers establish
// what happens; only this package has an opinion about it.
//
// The opinion rests on three questions, none of which look at command text:
// whose is it (ownership), how much is it (magnitude), and who else is
// touched (reach). They are thresholds for attention, not walls: an agent
// that did mean it says so with an ack and goes ahead.
package judge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"meant/internal/plat"
)

type Limits struct {
	OutsideEntries int `json:"outside_entries"` // entries changed outside home turf before it needs an ack
	LostFiles      int `json:"lost_files"`      // files whose uncommitted content may vanish before it needs an ack
}

// Declaration tells meant about an effect it cannot observe: a program whose
// consequences land in a database, a cluster, another machine.
type Declaration struct {
	Cmd   string   `json:"cmd"`             // program name
	Args  []string `json:"args,omitempty"`  // regexps; each must match some argument
	Says  string   `json:"says"`            // what it does, in a sentence
	Level string   `json:"level,omitempty"` // "hold" (default) or "note"
}

type Config struct {
	Zones   []string      `json:"zones,omitempty"` // extra directories that count as the agent's own
	Limits  Limits        `json:"limits"`
	Declare []Declaration `json:"declare,omitempty"`
}

// World is the frame of reference a command is judged in.
type World struct {
	Workspace string
	Home      string
	Zones     []string // temp and cache trees, plus configured ones
	UID       int
	Session   string
	CmdDir    string
	RootPID   int
	Limits    Limits
	Declare   []Declaration
}

func loadConfig(workspace string) Config {
	cfg := Config{Limits: Limits{OutsideEntries: 50, LostFiles: 3}}
	cfg.Declare = append(cfg.Declare, builtinDeclarations...)
	files := []string{filepath.Join(plat.StateRoot(), "config.json")}
	if workspace != "" {
		files = append(files, filepath.Join(workspace, ".meant.json"))
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var c Config
		if err := json.Unmarshal(raw, &c); err != nil {
			// A config that does not parse must not silently mean "defaults".
			os.Stderr.WriteString("meant: ignoring " + f + ": " + err.Error() + "\n")
			continue
		}
		cfg.Zones = append(cfg.Zones, c.Zones...)
		cfg.Declare = append(cfg.Declare, c.Declare...)
		if c.Limits.OutsideEntries > 0 {
			cfg.Limits.OutsideEntries = c.Limits.OutsideEntries
		}
		if c.Limits.LostFiles > 0 {
			cfg.Limits.LostFiles = c.Limits.LostFiles
		}
	}
	return cfg
}

func NewWorld(workspace, session, cmdDir string, rootPID int) *World {
	cfg := loadConfig(workspace)
	home := plat.Resolve(plat.RealHome())
	w := &World{
		Workspace: plat.Resolve(workspace),
		Home:      home,
		UID:       plat.Who(),
		Session:   session,
		CmdDir:    cmdDir,
		RootPID:   rootPID,
		Limits:    cfg.Limits,
		Declare:   cfg.Declare,
	}
	// A workspace that swallows the home directory is no boundary at all.
	if w.Workspace != "" && plat.Within(home, w.Workspace) {
		w.Workspace = ""
	}
	zones := plat.TempDirs()
	if c, err := os.UserCacheDir(); err == nil {
		zones = append(zones, c, filepath.Join(home, ".cache"))
	}
	for _, z := range append(zones, cfg.Zones...) {
		if strings.HasPrefix(z, "~") {
			z = filepath.Join(home, z[1:])
		}
		if z = plat.Resolve(z); z != "" && z != string(os.PathSeparator) {
			w.Zones = append(w.Zones, z)
		}
	}
	return w
}

// Save writes the world next to the command's records, so that the many
// short-lived shims of a command read one small file instead of each
// resolving homes, zones and config again.
func (w *World) Save() {
	raw, _ := json.Marshal(w)
	os.WriteFile(filepath.Join(w.CmdDir, "world.json"), raw, 0o644)
}

// FromEnv gives a shim the world its command runs in, or nil when the shim
// was reached outside any meant command.
func FromEnv() *World {
	cmdDir := os.Getenv(plat.EnvCmd)
	if cmdDir == "" {
		return nil
	}
	w := &World{}
	if raw, err := os.ReadFile(filepath.Join(cmdDir, "world.json")); err == nil && json.Unmarshal(raw, w) == nil {
		return w
	}
	root, _ := strconv.Atoi(os.Getenv(plat.EnvRoot))
	return NewWorld(os.Getenv(plat.EnvWS), os.Getenv(plat.EnvSession), cmdDir, root)
}

type turf int

const (
	outside   turf = iota
	scratch        // temp and cache trees: the agent's to churn
	workspace      // the project: every change is snapshotted and can be put back
)

func (w *World) turfOf(path string) turf {
	if plat.Within(path, w.Workspace) {
		return workspace
	}
	for _, z := range w.Zones {
		if plat.Within(path, z) {
			return scratch
		}
	}
	return outside
}

func groupsFile(session string) string {
	return filepath.Join(plat.StateRoot(), "sessions", session, "groups")
}

// groups lists the process groups this session's commands have run in.
func groups(session string) []int {
	raw, _ := os.ReadFile(groupsFile(session))
	var out []int
	for _, f := range strings.Fields(string(raw)) {
		if n, err := strconv.Atoi(f); err == nil && n > 1 {
			out = append(out, n)
		}
	}
	return out
}

// KeepGroups records a command's process group. Given the process table, it
// also forgets groups with no live member, so that a recycled group id is
// not mistaken for the session's.
func KeepGroups(session string, add int, procs map[int]plat.Proc) {
	live := map[int]bool{}
	for _, p := range procs {
		live[p.PGID] = true
	}
	var keep []string
	seen := map[int]bool{}
	for _, g := range append(groups(session), add) {
		if g > 1 && !seen[g] && (procs == nil || live[g] || g == add) {
			seen[g] = true
			keep = append(keep, strconv.Itoa(g))
		}
	}
	os.WriteFile(groupsFile(session), []byte(strings.Join(keep, "\n")+"\n"), 0o644)
}
