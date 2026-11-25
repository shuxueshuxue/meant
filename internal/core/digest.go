package core

import (
	"fmt"
	"strings"
)

// Digest is what a command's transaction says back to the agent. It is
// appended to the command's own output, so it has to earn every line:
// ordinary commands get nothing or one line.
type Digest struct {
	Effects []Effect // from this command
	Pending []Effect // surprising effects from earlier commands, still unresolved
	Blind   []string // things the observers could not look at this time
	Host    string   // set when this digest is written on a machine reached over ssh
}

func (d Digest) Empty() bool {
	return len(d.Effects) == 0 && len(d.Pending) == 0 && len(d.Blind) == 0
}

func (d Digest) Render() string {
	var b strings.Builder
	var notes []string
	for _, e := range d.Effects {
		switch {
		case e.Held && e.Remote != "":
			fmt.Fprintf(&b, "meant: STOPPED on %s; its report is just above. Nothing after `%s` ran here either.\n", e.Remote, Clip(e.Act, 80))
			fmt.Fprintf(&b, "  Meant it? Put the `ack <code>` that report names in front of this whole command and run it again.\n")
		case e.Held:
			fmt.Fprintf(&b, "meant: STOPPED. The command was cut off before this took effect:\n")
			writeBody(&b, e)
			fmt.Fprintf(&b, "  Everything before it in the command already ran; nothing after it did.\n")
			fmt.Fprintf(&b, "  Meant it? Run it again with `ack %s` in front (`ack %s && ...`). Did not? Fix the command.\n", e.Code(), e.Code())
		case e.Level == Surprise && !e.Acked:
			fmt.Fprintf(&b, "meant: this command did something you may not have meant:\n")
			writeBody(&b, e)
			fmt.Fprintf(&b, "  Meant it? Put `ack %s &&` in front of your next command; no separate call is needed. Did not? `reject %s` takes it back.\n", e.Code(), e.Code())
		default:
			notes = append(notes, noteLine(e))
		}
	}
	if len(notes) > 0 {
		fmt.Fprintf(&b, "meant: %s\n", strings.Join(notes, " · "))
	}
	for _, e := range d.Pending {
		fmt.Fprintf(&b, "meant: still unresolved from an earlier command [%s]: %s. `ack %s` or `reject %s`.\n",
			e.Code(), firstFact(e), e.Code(), e.Code())
	}
	if len(d.Blind) > 0 {
		fmt.Fprintf(&b, "meant: not observed this time: %s\n", strings.Join(d.Blind, "; "))
	}
	// Seen from the machine the agent is on, every line must say where it
	// happened.
	if d.Host != "" {
		return strings.ReplaceAll(b.String(), "meant: ", "meant@"+d.Host+": ")
	}
	return b.String()
}

func writeBody(b *strings.Builder, e Effect) {
	if e.Act != "" {
		fmt.Fprintf(b, "  %s\n", Clip(e.Act, 200)) // a glob can expand to thousands of words
	}
	for _, f := range e.Facts {
		fmt.Fprintf(b, "    %s\n", f)
	}
	for _, w := range e.Why {
		fmt.Fprintf(b, "    ! %s\n", w)
	}
}

func noteLine(e Effect) string {
	if e.Acked {
		return "went ahead as acknowledged: " + Clip(e.Act, 160)
	}
	return firstFact(e)
}

func firstFact(e Effect) string {
	if len(e.Facts) > 0 {
		return e.Facts[0]
	}
	return e.Act
}
