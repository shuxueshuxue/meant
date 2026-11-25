# How meant got its shape

This is the record of the trade-offs, the wrong turns, and the things learned
only by building it. The README says what meant does; this says why it does
that and not something else.

## The one sentence

**The danger is the agent's mistaken belief: it thinks the command will do A,
and the command does B.** So the tool does one thing. Before B becomes
irreversible, it puts B in front of the agent in concrete numbers and names.
It asks the agent to say `ack` or `reject` only when B does not look like
what was meant.

## What was settled before any code

- Defend against mistakes, not adversaries. No VM, no isolation layer, and no
  allowlist: a mistaken agent passes every "is this allowed?" check.
- Attach at the layer where the harness runs commands, lightly.
- Report effects with a short code, and ask for an ack only when something is
  surprising. The code is a read receipt: it is a hash of the effect, so it
  changes when the targets change.
- Use several observers, each with its own precision and cost, rather than one
  method.
- Whatever can be seen at run time is not predicted ahead of time. There is no
  shell parser anywhere in the project.
- Whose it is, how much, and who else it reaches are thresholds for
  attention, not walls.
- What cannot be observed is said to be unobserved, not guessed at.
- No model calls, no declarations written by the agent, no configuration
  needed.

## Where the implementation departed from the plan

### No overlay underneath

The plan ended with the whole session running in an overlay view, so that
every effect would be reversible by construction. It was not built that way:

- Unprivileged user namespaces are blocked by AppArmor on current Ubuntu, and
  many agents run as root anyway. An overlay is not "install and go" even on
  Linux.
- macOS and Windows have no equivalent, and the requirement was three
  platforms and any harness.
- The overlay's hard problems are structural. An IDE or dev server sees the
  old files, and docker or systemctl act outside the view.

The question was turned around: *without changing the world a command sees,
can the agent still see an effect before it becomes final?* That splits
irreversibility in two:

1. **What can be put back** (files in the workspace): let it happen, look at
   the net result, restore on request. This is the snapshot.
2. **What cannot** (deletes outside the workspace, ownership and mode changes,
   signals, forced pushes): catch it at the moment it happens. These are the
   effect points.

The host always holds the real state, so the IDE sees what the agent sees.
The price is that the second kind can only be stopped, not done and undone.

### A stop stops the whole command

A real incident command looks like
`rm -rf "$HOME" 2>/dev/null; rm -f /tmp/x.log; cd proj && git push origin main`.
If only the `rm` failed, the push would run on a false premise. So when an
effect point holds, the runner tears down every process the command started.
Nothing after that point runs, and the report says exactly that.

Two early bugs shaped this:

- **The shell kept going.** The first stand-in reported and exited, so the
  shell ran the next statement before the kill arrived. Now a stand-in that
  holds never returns; it waits to be torn down with the rest of the command.
- **Reports were swallowed.** `2>/dev/null` hid the stand-in's stderr, and
  nearly every incident command carries one. Now the runner prints the
  report, after the command and outside its redirections.

The cost is real. When the effect was meant, a stop costs one extra tool call:
the agent reruns the command under the ack, and the steps before the stop run
twice. The README states this rather than hiding it.

### "Surprising" means work destroyed, not files changed

The first snapshot rule counted deleted files. Switching branches deletes
hundreds of files and is fine, so the rule became: **content that existed
only in the working tree is now gone.** That means the old content is in no
commit, index, or stash, and the new content is absent, empty, or a version
git already has. Ordinary edits, branch switches, and stashes do not count.

A test caught a subtle hole. After `git stash && git stash drop`, the blobs
are still in the object store, dangling. Checking for an object's existence
called them safe. The check is now by **reachability**: the usual places
first, then which ref can reach the object.

The rule does not care who did it. `git reset --hard`, `git clean`, a Python
script, and a stray redirection are all seen, and all can be restored with
`reject`.

### `ack` is an ordinary command

The plan had the harness recognise an `ack 7f3a` prefix. That turned out to be
impossible. Through `CLAUDE_CODE_SHELL_PREFIX` the wrapper does not receive
the agent's command, only a string the harness has assembled around it. So
`ack` and `reject` became two real programs on `PATH`. `ack 7f3a && make
deploy` is plain shell. The whole protocol lives in command text and output,
and the harness only hands commands over.

## Learned by attaching to Claude Code

- **A `PreToolUse` rewrite is the wrong entry point.** Permission rules are
  evaluated against the rewritten command, so every `Bash(npm test:*)`-style
  rule stops matching. `CLAUDE_CODE_SHELL_PREFIX` leaves the permission layer
  looking at the agent's own command.
- **The prefix must be a bare executable.** `meant wrap` broke every Bash
  call in the first live session. The agent's diagnosis was "Bash is failing
  because of a broken command wrapper." Install now links a `meant-wrap`
  alias, and the binary dispatches on `argv[0]`.
- **The prefix wraps every shell-out**: hooks, status lines, MCP servers.
  Only Bash tool calls are the agent's, so `meant-wrap` recognises their shape
  and execs everything else untouched.
- **The harness's shell snapshot resets `PATH`, silently.** In the second live
  test a cleanup script deleted 60 files outside the workspace and nothing
  was said, because the stand-ins were no longer on `PATH`. Now a `DEBUG`
  trap puts the stand-in directory back whenever `PATH` loses it. The first
  version of the trap broke `$_`; it now hands `$_` back unchanged.
  Lesson: **"it is attached" is proven only by a command that should be
  stopped, being stopped.**
- In bash, `command kill` still runs the builtin. A shell function that calls
  the stand-in by absolute path is what routes it.

## Live agents

All runs used Claude Code with a Sonnet model, told nothing about meant beyond
the three-sentence primer the `SessionStart` hook injects.

1. **Fix a bug and clean the shared store.** The agent had already spotted
   that the cleanup script deletes every lane when `LANE` is unset, and set
   `LANE=lane7` itself. It was stopped: "would delete 60 files … outside your
   workspace". It answered "this is exactly the deletion you asked for, and I
   set the scope myself", and reran with `ack`.
2. **Free a port and drop half-done work.** `kill` was stopped with "not
   started by this session: python3 -m http.server". The agent acked, and in
   its final report added on its own that the process had been running for
   ten seconds and might not have been stale. Without the report it would not
   have known it was killing someone else's process.
3. **`git checkout . && git clean -fd`.** It ran, then was flagged: "4 files
   lost content that was never committed". The user had asked for it, so the
   agent acked, and **listed the four lost files for the user**. This is the
   behaviour the tool exists for: what the agent says matches what happened.

No habitual acking was seen, but the sample is small, and the thresholds (50
entries outside the workspace, 3 lost files) are guesses that real sessions
should calibrate.

## The author's own mistakes

The same mistake, five times, while building this: a `pkill -f` or
`pgrep -f` pattern matched the shell that was running it, because that
shell's command line contains the pattern.

- Four times the shell died: three outside meant, and once inside it, through
  the `command kill` bug above.
- Once meant saw it and stopped it: `1 of 2 targets were not started by this
  session: 2463042 /bin/bash -c …`.
- In the same round, `pgrep -x sleep` matched two `sleep` processes belonging
  to other users' sessions. meant stopped that kill as well.

Knowing about the mistake did not prevent it; the command text looks fine.
The test suite now takes victims' pids from `$!`, never from a pattern.

## Lines held in the architecture

- **Verbs resolve; the judge decides.** Each verb answers only "what does
  this invocation concretely touch". Every threshold lives in `judge`.
  Adding a verb does not require knowing the thresholds, and changing a
  threshold does not touch any verb.
- **The judge never sees a command name.** `rm -rf build/` and `rm -rf ~` go
  through the same code and get different answers from their effects.
- **Declarations are data, and say they are declarations.** Database and
  cluster effects are invisible locally, so a short table names them
  (`drizzle-kit push`, `terraform destroy`, …). It looks like a deny-list, so
  it is kept narrow: only what can be neither seen nor undone. The report
  labels it as declared, not observed, and users extend it in `.meant.json`.
- **Platform differences live in `plat` alone.** Everything above asks about
  processes, owners, and links, and gets the same shape of answer everywhere.
- **Failures are loud.** A snapshot that is too slow turns itself off and says
  so; a config that does not parse is reported; each blind spot is told once
  per session. The one deliberate pass-through is a bug in a verb: it must not
  break the user's command, so the command runs and the digest says it ran
  unobserved.
- **One binary, dispatched on `argv[0]`.** The stand-ins, the wrapper, and the
  CLI are one file; installing means making links.

## Following ssh

A week of one team's agent sessions showed that 40% of shell calls went
through ssh, scp, or rsync. So did 71% of the calls that named `rm`, `kill`,
`chmod`, or a forced push. Most real damage was one hop away.

- The local `ssh` stand-in joins the remote words the way ssh does, and
  treats the result as one opaque string. It quotes the string once and sends
  `sh -c '<start meant there>' '<original>'`. The original arrives as `$0`,
  so nothing is quoted twice and nothing is parsed.
- The far side runs it in the shell sshd would have used, as an ordinary
  transaction. Its report comes back over ssh's stderr, prefixed with its
  host.
- Acks already given travel with the command, and so does the session id: a
  process this session started over there is still its own.
- A stop over there exits 113, and the near side stops the rest of its
  command.
- Multi-hop works by itself, because the far meant has an `ssh` stand-in
  too.

The fear was quoting. Ten hostile remote commands were compared with and
without meant: nested quotes, `$()`, backticks, here-strings, exit codes,
stderr. The only difference was `$0`, which is now the short shell name, as
under sshd.

**Unknown hosts.** At first, a host where meant was not installed ran
unchanged with a one-line note. That was the wrong default: every rule ended
at the first ssh, and a note is easily skimmed. Now there are three cases:

- **Added** with `meant remote add`: followed and observed there.
- **Allowed** with `meant remote allow`: runs unobserved, and says so in one
  line.
- **Neither**: stops, and the report names both commands, or `ack` for this
  one call.

This is not the allowlist the design rejects. That one would list *commands*.
This one lists *channels* someone has knowingly accepted as unobservable.

## A mechanism tried and removed

To spare the extra tool call a stop costs, deletes that reached nobody else
were briefly done as renames into a holding area. The command carried on with
real results, `ack` finished the delete, and `reject` renamed the files back.
In a like-for-like live run that took 3 shell calls instead of 4.

It was removed after a day:

- **It only ever served deletes.** Signals and pushes cannot be done and then
  undone.
- **It needed a lot of machinery.** It brought a third protocol word
  (`later`), a guard holding irreversible actions while anything was
  unanswered, holding directories, and an opt-out flag: about 430 lines.
- **It had two real defects.** Disk space was not freed until the ack. When
  the holding area had to sit beside the target, `ls` and globs could see it.

A mechanism that serves one kind of effect and needs its own protocol words
and state is paying the difference for not having an overlay. Stating that
cost was cheaper than paying it.

## Not done, not verified

- **Not a sandbox.** Absolute `/bin/rm`, a program's own `unlink`, and
  `shutil.rmtree` all walk past the effect points. Inside the workspace the
  snapshot still sees the result; outside it, nothing does.
- **Remote user switches** (`su`, `runuser`, lane-entry scripts) reset `PATH`,
  and the stand-ins are lost from there on. **scp and rsync** are not
  followed. **A remote pipe** can cut the far side's report, because it
  travels on stderr. **Exit status 113** may collide with another program's.
- **Network writes** are not held; `git push` is handled only by asking git
  what a push would discard.
- **Overwrites by `mv` or `cp`** outside the workspace are not modelled.
- **sudo** forwarding is written but untested. **Codex** is written against
  the documented hook protocol and has never run on a live Codex. **Root**
  was never available, so the other-users'-files rules are untested as root.
- **Parallel tool calls** share one snapshot index. A lock keeps it correct,
  but attribution between simultaneous commands is blurry.
- **Each stood-in call costs 3–4 ms** (Go start-up plus one more `exec`). A
  `make` that runs `rm` a thousand times will notice.
- **Git-ignored files are not snapshotted**, so deleting an ignored `.env`
  goes unremarked.

## Platforms

**Linux (bash).** Unit tests, the incident suite, live sessions, and ssh to a
second host.

**macOS (arm64, zsh).** The first run of the incident suite failed 4 of 34,
all real bugs that no cross-compile or vet could find:

- meant's own `ps` was reported as a leftover process.
- **macOS does not let other processes read a system binary's environment**,
  so the environment marker, the proudest mechanism on Linux, was nearly
  blind there.
- `ps -E` mixed environment values into the command line it showed.

The fix was a process group per command, with ownership established by group
or marker, whichever holds. After the fixes the suite passed 34/34 with zsh as
the shell. macOS has since been the far side of ssh from Linux, including in a
live agent session. *A mechanism verified on one platform is a mechanism for
that platform.*

**Windows.** It cross-compiles and passes vet, and has **never been run**.

## Lessons

1. **A different threat model changes which tools are good.** As security, a
   `PATH` stand-in is a joke: one absolute path walks past it. If the agent is
   not trying to walk past it, it is the only place on three platforms that
   needs no privilege, no kernel, and no altered view, and still sees the
   **real arguments after expansion**.
2. **The useful message is a concrete number.** "This is dangerous" tells an
   agent nothing it did not know. "would delete 90 files in 3 dirs under
   /srv/store: lane1, lane2, lane7" contradicts the belief "I am deleting
   lane7", directly.
3. **Look after the fact at what can be undone; stop what cannot.** That one
   line keeps both mechanisms simple, and keeps acks rare.
4. **"It is attached" is the easiest state to fool yourself about.** When
   `PATH` was being reset, every ordinary command still worked and the digest
   still appeared. Only the command that should have been stopped was not.
5. **Real agents read the report and carry its facts into what they tell the
   user.** The tool's job is not to decide for them. It is to make sure they
   do not decide without knowing.
