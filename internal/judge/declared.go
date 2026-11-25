package judge

import (
	"path/filepath"
	"regexp"
	"strings"

	"meant/internal/core"
)

// builtinDeclarations cover effects no local observer can see because they
// land somewhere else: a database, a cluster, a disk device, another host.
// This is deliberately a short table of data, not code, and not a list of
// "dangerous commands": each entry is here because its consequence is both
// invisible from this machine and not undoable from it. Projects add their
// own in .meant.json.
var builtinDeclarations = []Declaration{
	{Cmd: "drizzle-kit", Args: []string{"^push$"}, Says: "pushes the schema straight into the database, dropping whatever does not match"},
	{Cmd: "prisma", Args: []string{"^migrate$", "^reset$"}, Says: "drops and recreates the database"},
	{Cmd: "prisma", Args: []string{"^--accept-data-loss$"}, Says: "applies schema changes that the tool itself says lose data"},
	{Cmd: "terraform", Args: []string{"^destroy$"}, Says: "destroys the infrastructure in this state"},
	{Cmd: "terraform", Args: []string{"^apply$", "^-auto-approve$"}, Says: "changes real infrastructure without showing the plan to anyone"},
	{Cmd: "kubectl", Args: []string{"^delete$"}, Says: "deletes resources in whatever cluster the current context points at"},
	{Cmd: "dropdb", Says: "drops a database"},
	{Cmd: "docker", Args: []string{"^(system|volume)$", "^(prune|rm)$"}, Says: "removes docker volumes; the data in them is gone"},
	{Cmd: "dd", Args: []string{"^of=/dev/"}, Says: "writes raw bytes over a device"},
	{Cmd: "shred", Says: "overwrites files so that they cannot be recovered"},
	{Cmd: "ssh", Args: []string{"^[^-].*", "."}, Level: "note", Says: "ran on another machine; nothing it did there was observed"},
	{Cmd: "systemctl", Args: []string{"^(start|stop|restart|reload|enable|disable|mask|kill)$"}, Level: "note", Says: "asked systemd to change a service; what the service does is not observed"},
	{Cmd: "crontab", Args: []string{"^(-r|-|[^-].*)$"}, Level: "note", Says: "replaced or removed this user's crontab"},
}

// Declared matches argv against the table. Arguments are the real ones, as
// the program received them after every layer of shell expansion.
func (w *World) Declared(argv []string) *core.Effect {
	name := strings.TrimSuffix(filepath.Base(argv[0]), ".exe")
	for _, d := range w.Declare {
		if d.Cmd != name || !matchAll(d.Args, argv[1:]) {
			continue
		}
		e := &core.Effect{Kind: core.Declared, Act: strings.Join(argv, " "), Level: core.Surprise}
		if d.Level == "note" {
			e.Kind, e.Level = core.Unseen, core.Note
			e.Facts = []string{name + ": " + d.Says}
			return e
		}
		e.Facts = []string{d.Says}
		e.Why = []string{"meant cannot see or undo this: its effect lands outside this machine's files and processes"}
		e.Key = core.MakeKey(core.Declared, argv...)
		return e
	}
	return nil
}

// matchAll reports whether every pattern matches a different-or-same later
// argument, in order.
func matchAll(patterns, args []string) bool {
	i := 0
	for _, pat := range patterns {
		re, err := regexp.Compile(pat)
		if err != nil {
			return false
		}
		found := false
		for ; i < len(args); i++ {
			if re.MatchString(args[i]) {
				found = true
				i++
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// DeclaredCommands lists the program names that need a shim.
func DeclaredCommands(decls []Declaration) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range decls {
		if !seen[d.Cmd] {
			seen[d.Cmd] = true
			out = append(out, d.Cmd)
		}
	}
	return out
}
