// Package harness is the thin edge that attaches meant to an agent harness.
// Each harness has exactly one job to do: hand every shell command to
// `meant run`. Everything else, the ack protocol included, travels inside
// ordinary command text and output, so no harness needs to understand it.
package harness

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"meant/internal/core"
	"meant/internal/ledger"
	"meant/internal/plat"
	"meant/internal/run"
)

// bashTool recognises the invocation Claude Code assembles for a Bash tool
// call: environment setup, then `eval '<the command>'`, then bookkeeping.
var bashTool = regexp.MustCompile(`(?s)\beval '(.*)' < /dev/null && pwd -P >\| `)

// Wrap is the target of CLAUDE_CODE_SHELL_PREFIX (as the meant-wrap alias). Claude Code routes several
// kinds of shell-outs through the prefix; only Bash tool calls are agent
// commands. Hooks, status lines and MCP servers pass through untouched.
func Wrap(program string) int {
	if m := bashTool.FindStringSubmatch(program); m != nil {
		return run.Run(run.Options{Program: program, Display: strings.ReplaceAll(m[1], `'\''`, `'`)})
	}
	if strings.Contains(program, "shell-snapshots/") {
		// A Bash tool call in a shape this version does not know. Observe it
		// anyway rather than letting an upgrade silently switch meant off.
		return run.Run(run.Options{Program: program})
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	if err := plat.Exec(shell, []string{shell, "-c", program}); err != nil {
		fmt.Fprintf(os.Stderr, "meant: %v\n", err)
	}
	return 126
}

// Hook answers the hook protocol that Claude Code and Codex share. It reads
// one JSON event on stdin and may print one JSON decision.
//
//	SessionStart  tell the agent, in three lines, what meant's messages are
//	Stop          do not let a turn end on an effect nobody acknowledged
//	PreToolUse    (harnesses without a shell prefix) rewrite the command
func Hook() int {
	raw, _ := io.ReadAll(os.Stdin)
	var ev struct {
		Event      string                 `json:"hook_event_name"`
		Session    string                 `json:"session_id"`
		Cwd        string                 `json:"cwd"`
		StopActive bool                   `json:"stop_hook_active"`
		Tool       string                 `json:"tool_name"`
		Input      map[string]interface{} `json:"tool_input"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		fmt.Fprintf(os.Stderr, "meant hook: bad event: %v\n", err)
		return 1
	}
	if ev.Cwd != "" {
		os.Chdir(ev.Cwd)
	}
	sess := ledger.Open(ledger.SessionID(ev.Session, run.FindWorkspace()))
	emit := func(v interface{}) int {
		json.NewEncoder(os.Stdout).Encode(v)
		return 0
	}
	switch ev.Event {
	case "SessionStart":
		return emit(map[string]interface{}{"hookSpecificOutput": map[string]interface{}{
			"hookEventName": "SessionStart", "additionalContext": primer + pendingText(sess.Pending())}})
	case "Stop":
		pending := sess.Pending()
		if len(pending) == 0 {
			return 0
		}
		if ev.StopActive {
			// Already asked once this turn. Let the turn end, and make sure
			// the person sees what the agent did not acknowledge.
			return emit(map[string]interface{}{"systemMessage": "meant: unacknowledged effects remain." + pendingText(pending)})
		}
		return emit(map[string]interface{}{"decision": "block",
			"reason": "meant: before you finish, resolve what your commands did that you have not acknowledged." + pendingText(pending) +
				"\nRun `ack <code>` if you meant it or `reject <code>` to take it back, and tell the user what happened."})
	case "PreToolUse":
		cmd, _ := ev.Input["command"].(string)
		if cmd == "" || strings.HasPrefix(strings.TrimSpace(cmd), "meant ") {
			return 0
		}
		self, _ := os.Executable()
		ev.Input["command"] = core.Quote(self) + " run -s " + core.Quote(sess.ID) + " -c " + core.Quote(cmd)
		return emit(map[string]interface{}{"hookSpecificOutput": map[string]interface{}{
			"hookEventName": "PreToolUse", "updatedInput": ev.Input}})
	}
	return 0
}

// Primer is everything an agent needs to be told up front. The digests
// explain themselves; this only says they are to be believed.
const primer = "Shell commands in this session run under `meant`, which reports what a command actually did when that may differ from what you intended. " +
	"Lines starting with `meant:` in command output are observations of real effects, not errors from the command. " +
	"If one says a command was STOPPED or did something you may not have meant, read it, then either fix your command or reply with the `ack <code>` / `reject <code>` command it names."

func pendingText(pending []core.Effect) string {
	var b strings.Builder
	for _, e := range pending {
		fmt.Fprintf(&b, "\n  [%s] %s", e.Code(), strings.Join(e.Facts, "; "))
	}
	return b.String()
}

// Install writes the configuration that attaches meant to a harness and
// returns what it did, in words. It merges into existing settings and never
// replaces a key it does not own.
func Install(target, scope string) (string, error) {
	self, err := plat.Self()
	if err != nil {
		return "", err
	}
	hook := func() map[string]interface{} {
		return map[string]interface{}{"hooks": []interface{}{map[string]interface{}{"type": "command", "command": core.Quote(self) + " hook"}}}
	}
	switch target {
	case "claude-code", "claude":
		file := filepath.Join(".claude", "settings.local.json")
		if scope == "user" {
			dir := os.Getenv("CLAUDE_CONFIG_DIR")
			if dir == "" {
				dir = filepath.Join(plat.RealHome(), ".claude")
			}
			file = filepath.Join(dir, "settings.json")
		}
		// The prefix has to be one executable path with no arguments, so the
		// wrapper is the same binary under another name.
		wrap := filepath.Join(plat.StateRoot(), plat.ShimName("meant-wrap"))
		os.MkdirAll(plat.StateRoot(), 0o755)
		os.Remove(wrap)
		if err := plat.Link(self, wrap); err != nil {
			return "", err
		}
		err := merge(file, func(cfg map[string]interface{}) {
			env := object(cfg, "env")
			env["CLAUDE_CODE_SHELL_PREFIX"] = wrap
			hooks := object(cfg, "hooks")
			for _, ev := range []string{"SessionStart", "Stop"} {
				hooks[ev] = addHook(hooks[ev], hook())
			}
		})
		return "Claude Code: every Bash tool call now runs through meant-wrap (CLAUDE_CODE_SHELL_PREFIX), with SessionStart and Stop hooks, in " + file, err
	case "codex":
		file := filepath.Join(plat.RealHome(), ".codex", "hooks.json")
		if scope != "user" {
			file = filepath.Join(".codex", "hooks.json")
		}
		err := merge(file, func(cfg map[string]interface{}) {
			hooks := object(cfg, "hooks")
			pre := hook()
			pre["matcher"] = "^(Bash|shell|local_shell|exec_command)$"
			hooks["PreToolUse"] = addHook(hooks["PreToolUse"], pre)
			for _, ev := range []string{"SessionStart", "Stop"} {
				hooks[ev] = addHook(hooks[ev], hook())
			}
		})
		return "Codex: a PreToolUse hook now rewrites shell commands to `meant run`, with SessionStart and Stop hooks, in " + file, err
	}
	return "", fmt.Errorf("unknown harness %q (known: claude-code, codex; any other: make it run `meant run -c '<command>'`)", target)
}

func merge(file string, edit func(map[string]interface{})) error {
	cfg := map[string]interface{}{}
	if raw, err := os.ReadFile(file); err == nil {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return fmt.Errorf("%s is not valid JSON; not touching it: %v", file, err)
		}
	}
	edit(cfg)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(cfg, "", "  ")
	return os.WriteFile(file, append(raw, '\n'), 0o644)
}

func object(cfg map[string]interface{}, key string) map[string]interface{} {
	if m, ok := cfg[key].(map[string]interface{}); ok {
		return m
	}
	m := map[string]interface{}{}
	cfg[key] = m
	return m
}

// addHook appends meant's hook group unless one is already there.
func addHook(existing interface{}, group map[string]interface{}) []interface{} {
	list, _ := existing.([]interface{})
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "meant") && strings.Contains(string(raw), " hook") {
		return list
	}
	return append(list, group)
}
