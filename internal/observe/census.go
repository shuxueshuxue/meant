package observe

import (
	"fmt"
	"os"
	"sort"

	"meant/internal/core"
	"meant/internal/plat"
)

// Leftovers finds the processes a command started, from this runner, that
// are still there. They are identified by the marker the command put in their
// environment or by the process group it ran in, so nohup, `&` and (where
// markers can be read) setsid do not hide them. Descent from the runner is
// the last resort.
func Leftovers(procs map[int]plat.Proc, cmdDir string, group int) []plat.Proc {
	runner := os.Getpid()
	var left []plat.Proc
	for _, p := range procs {
		if p.PID == runner {
			continue
		}
		if (cmdDir != "" && p.CmdDir == cmdDir) || (group > 1 && p.PGID == group) || plat.Descends(procs, p.PID, runner) {
			left = append(left, p)
		}
	}
	sort.Slice(left, func(i, j int) bool { return left[i].PID < left[j].PID })
	return left
}

// LeftoverEffect says in one line what is still running.
func LeftoverEffect(left []plat.Proc) *core.Effect {
	if len(left) == 0 {
		return nil
	}
	var names []string
	for _, p := range left {
		names = append(names, fmt.Sprintf("%s [%d]", core.Clip(p.Cmd, 48), p.PID))
	}
	return &core.Effect{Kind: core.Leftover, Level: core.Note,
		Facts: []string{"still running: " + core.List(names, 4)}}
}
