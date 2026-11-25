package verbs

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"meant/internal/core"
	"meant/internal/judge"
	"meant/internal/ledger"
	"meant/internal/plat"
)

// A Remote is a host commands may be sent to by ssh. Either meant has been
// put there, so they are observed there, or the user has said that commands
// may go there unobserved (Path is empty). Any other host is unknown, and a
// command sent to it stops until someone decides which of the two it is.
type Remote struct {
	Host  string // the destination as typed on the ssh command line, without user@
	Path  string // where the binary is on that host; empty: allowed unobserved
	State string // state root there; empty for that host's default
}

func remotesFile() string { return filepath.Join(plat.StateRoot(), "remotes") }

func Remotes() []Remote {
	raw, _ := os.ReadFile(remotesFile())
	var out []Remote
	for _, line := range strings.Split(string(raw), "\n") {
		if f := strings.Split(line, "\t"); len(f) == 3 && f[0] != "" {
			out = append(out, Remote{f[0], f[1], f[2]})
		}
	}
	return out
}

// SetRemote records a host, replacing what was known about it.
func SetRemote(r Remote) error {
	return saveRemotes(append(without(Remotes(), r.Host), r))
}

// DropRemote forgets a host.
func DropRemote(host string) error { return saveRemotes(without(Remotes(), host)) }

func without(list []Remote, host string) []Remote {
	var keep []Remote
	for _, r := range list {
		if r.Host != host {
			keep = append(keep, r)
		}
	}
	return keep
}

func saveRemotes(list []Remote) error {
	var b strings.Builder
	for _, r := range list {
		fmt.Fprintf(&b, "%s\t%s\t%s\n", r.Host, r.Path, r.State)
	}
	os.MkdirAll(plat.StateRoot(), 0o755)
	return os.WriteFile(remotesFile(), []byte(b.String()), 0o644)
}

// sshWithValue lists the ssh options that take an argument.
const sshWithValue = "BbcDEeFIiJLlmOoPpQRSWw"

// sshNoCommand lists the options under which ssh does not run a remote
// shell command at all (forwarding only, control commands, subsystems).
const sshNoCommand = "NWOQVGs"

// splitSSH separates an ssh command line into its options, destination and
// remote command words. ok is false whenever the line is anything but a
// plain "run this command on that host": the caller then leaves it alone.
func splitSSH(argv []string) (opts []string, dest string, command []string, ok bool) {
	i := 1
	for i < len(argv) {
		a := argv[i]
		if a == "--" {
			i++
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			break
		}
		for j := 1; j < len(a); j++ {
			c := a[j]
			if strings.IndexByte(sshNoCommand, c) >= 0 {
				return nil, "", nil, false
			}
			if strings.IndexByte(sshWithValue, c) >= 0 {
				if j == len(a)-1 {
					i++ // the value is the next word
				}
				break
			}
			if !strings.ContainsRune("46AaCfgKkMnqTtvXxYy", rune(c)) {
				return nil, "", nil, false // an option this parser has never heard of
			}
		}
		i++
	}
	if i >= len(argv) {
		return nil, "", nil, false
	}
	return argv[1:i], argv[i], argv[i+1:], len(argv) > i+1
}

// HostOf is the host part of an ssh destination, which is how remotes are
// named.
func HostOf(dest string) string {
	dest = strings.TrimPrefix(dest, "ssh://")
	if at := strings.LastIndex(dest, "@"); at >= 0 {
		dest = dest[at+1:]
	}
	return dest
}

// relayCommand builds what is sent to the remote shell in place of the
// original command. The original is carried as one opaque string: it is
// never parsed or altered, only quoted once, and the remote meant hands it
// to the same shell ssh would have. If meant has gone missing over there the
// command still runs, and says loudly that nothing was watching.
func relayCommand(r Remote, session string, grants []string, original string) string {
	run := core.Quote(r.Path) + " run --relay -s " + core.Quote(session)
	if len(grants) > 0 {
		run += " -g " + core.Quote(strings.Join(grants, ","))
	}
	if r.State != "" {
		run = "MEANT_HOME=" + core.Quote(r.State) + " " + run
	}
	script := `if [ -x ` + core.Quote(r.Path) + ` ]; then ` + run + ` -c "$0"; else echo "meant: no meant at ` + r.Path +
		` on this host; this command ran unobserved" >&2; "${SHELL:-/bin/sh}" -c "$0"; fi`
	return "sh -c " + core.Quote(script) + " " + core.Quote(original)
}

// follow carries the observation across ssh. For a host where meant has been
// put, the remote command is wrapped so that it runs as a transaction over
// there: that machine's stand-ins see that machine's real arguments, files
// and processes. Acks already given in this command travel with it. If the
// remote command is stopped, this command is stopped as well.
//
// handled is false when the line is left for the ordinary path.
func follow(w *judge.World, argv []string, real string) (status int, handled bool) {
	opts, dest, command, ok := splitSSH(argv)
	if !ok || real == "" {
		return 0, false
	}
	var remote *Remote
	for _, r := range Remotes() {
		if r.Host == HostOf(dest) {
			remote = &r
			break
		}
	}
	if remote == nil {
		return unknownHost(w, argv, dest, command)
	}
	if remote.Path == "" {
		return 0, false // allowed unobserved: the declared note says so
	}
	// ssh joins the command words with spaces before the remote shell sees them.
	wrapped := relayCommand(*remote, w.Session, ledger.Grants(w.CmdDir), strings.Join(command, " "))
	args := append(append(append([]string{}, opts...), dest), wrapped)
	cmd := exec.Command(real, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "meant: cannot run %s: %v\n", real, err)
		return 126, true
	}
	if code == core.ExitRelayStopped {
		ledger.Record(w.CmdDir, core.Effect{Kind: core.Unseen, Level: core.Surprise, Held: true, Remote: HostOf(dest), Act: quote(argv)})
		waitForTeardown(w)
		return core.ExitStopped, true
	}
	return code, true
}

// unknownHost stops a command sent to a host nobody has decided about. What
// happens there would be invisible, and a silent pass would make every rule
// here end at the first ssh. The stop is not a verdict on the command: it
// asks for a decision about the channel, once per host, or an ack for this
// one call.
func unknownHost(w *judge.World, argv []string, dest string, command []string) (int, bool) {
	host := HostOf(dest)
	e := &core.Effect{Kind: core.Unseen, Level: core.Surprise, Act: quote(argv),
		Key: core.MakeKey(core.Unseen, "ssh", host, strings.Join(command, " ")),
		Facts: []string{"would run on " + host + ", where meant is not installed: nothing it does there would be observed",
			"`meant remote add " + host + "` puts meant there; `meant remote allow " + host + "` lets commands go there unobserved from now on"}}
	if !proceed(w, e) {
		return core.ExitStopped, true
	}
	return 0, false
}
