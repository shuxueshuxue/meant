package verbs

import (
	"fmt"
	"os"
	"regexp"

	"meant/internal/judge"
	"meant/internal/ledger"
	"meant/internal/plat"
)

var codeRE = regexp.MustCompile(`^[0-9a-f]{4}$`)

// resolve implements `ack <code>` and `reject <code>` as ordinary commands,
// so the protocol composes with the shell (`ack 7f3a && make deploy`) and
// needs no parsing by any harness.
//
// A code names either an effect that already happened and is waiting in the
// ledger, or one that was stopped. For the first, ack accepts it and reject
// puts things back. For the second, ack lets the same effect through for the
// rest of this command; reject has nothing to undo.
func resolve(w *judge.World, name string, argv []string, real string) int {
	if len(argv) < 2 || !codeRE.MatchString(argv[1]) || w == nil {
		if real != "" { // some other program called ack; stay out of its way
			plat.Exec(real, argv)
		}
		fmt.Fprintf(os.Stderr, "usage: %s <code>   (the 4-digit code from a meant message, inside an agent command)\n", name)
		return 2
	}
	for _, code := range argv[1:] {
		if !codeRE.MatchString(code) {
			fmt.Fprintf(os.Stderr, "meant: %q is not a code\n", code)
			return 2
		}
		s := ledger.Open(w.Session)
		if e, ok := s.Take(code); ok {
			if name == "ack" {
				note := ""
				if e.Undo != nil {
					note = "; `meant undo` can still restore it for about a week"
				}
				fmt.Printf("meant: acknowledged %s (%s)%s\n", code, e.Facts[0], note)
				continue
			}
			msg, err := ledger.Restore(e.Undo)
			if err != nil {
				s.Put(e) // still unresolved
				fmt.Fprintf(os.Stderr, "meant: could not take back %s: %v\n", code, err)
				return 1
			}
			fmt.Printf("meant: took back %s: %s\n", code, msg)
			continue
		}
		if name == "ack" {
			if err := ledger.Grant(w.CmdDir, code); err != nil {
				fmt.Fprintf(os.Stderr, "meant: %v\n", err)
				return 1
			}
		} else {
			fmt.Printf("meant: %s was stopped before it happened; there is nothing to take back\n", code)
		}
	}
	return 0
}
