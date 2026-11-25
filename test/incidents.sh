#!/usr/bin/env bash
# Replays real agent incidents, end to end, against the built binary.
#
# Each case is the shape of a command that destroyed something in a public
# report or in our own fleet, pointed at throwaway directories. A case passes
# when meant makes the agent look before the damage is permanent, and the
# ordinary commands around it run exactly as they would without meant.
#
#   test/incidents.sh [path/to/meant]
set -u
ROOT=$(cd "$(dirname "$0")/.." && pwd)
MEANT=${1:-$ROOT/bin/meant}
# The fixtures must not sit in a temp dir: temp dirs are the agent's own turf,
# and half of these cases are about deleting what is not.
case $ROOT/ in
  /tmp/*|/var/tmp/*|/private/tmp/*|"${TMPDIR:-/nonexistent}"/*)
    printf 'test/incidents.sh: %s is in a temp directory, where every delete is the agent'"'"'s own business.\n' "$ROOT" >&2
    printf 'Run it from a checkout outside /tmp, or set MEANT_LAB to a directory outside it.\n' >&2
    [ -n "${MEANT_LAB:-}" ] || exit 2 ;;
esac
LAB=${MEANT_LAB:-$ROOT/.lab}/e2e-$$
export MEANT_HOME=$LAB/state MEANT_SESSION=e2e
unset CLAUDE_CODE_SESSION_ID CLAUDE_PROJECT_DIR
G="git -c user.name=t -c user.email=t@t -c init.defaultBranch=main"
pass=0; fail=0
trap 'rm -rf "$LAB"' EXIT

ok()   { pass=$((pass+1)); printf '  ok    %s\n' "$1"; }
bad()  { fail=$((fail+1)); printf '  FAIL  %s\n        %s\n' "$1" "$2"; }
# run <command>: runs it under meant in $WS; sets $out (stdout+stderr) and $rc.
run()  { out=$(cd "$WS" && "$MEANT" run -c "$1" 2>&1); rc=$?; }
code() { printf '%s' "$out" | grep -o 'ack [0-9a-f]\{4\}' | head -1 | cut -d' ' -f2; }
check() { # check <name> <condition...>
  local name=$1; shift
  if "$@" >/dev/null 2>&1; then ok "$name"; else bad "$name" "$(printf '%s' "$out" | head -8)"; fi
}
stopped() { [ "$rc" = 125 ] && printf '%s' "$out" | grep -q 'meant: STOPPED'; }
says()    { printf '%s' "$out" | grep -q -- "$1"; }
silent()  { ! printf '%s' "$out" | grep -q '^meant:'; }

mkdir -p "$LAB" && WS=$LAB/ws && mkdir -p "$WS" && cd "$WS" && $G init -q . &&
  echo one > a.txt && echo two > b.txt && $G add -A && $G commit -qm init || exit 1

echo "transparency"
run 'echo hello; exit 7'
check "stdout and exit status pass through" [ "$rc" = 7 -a "$out" = hello ]
run 'ls >/dev/null; cat a.txt >/dev/null; git status --short'
check "read-only commands get no commentary" silent
run 'mkdir -p d && cd $_ && pwd'
check 'shell semantics survive ($_)' says "$WS/d"
run 'echo x >> a.txt; mkdir -p build && touch build/o1 build/o2 && rm -rf build'
check "ordinary edits are one line" says 'meant: files ~1 (a.txt)'
$G -C "$WS" checkout -q .

echo "deletes that were not where the agent thought (claude-code#75859, #93099, #88462; fleet F1)"
FAKE=$LAB/home-like; mkdir -p "$FAKE/Documents" && for i in $(seq 60); do echo $i > "$FAKE/Documents/f$i"; done
run "rm -rf \"$FAKE\" 2>/dev/null; echo AFTER"
check "rm -rf of a big tree outside the workspace is stopped" stopped
check "its errors-to-/dev/null did not hide the report" says "would delete 60 files"
check "the rest of the command did not run" bash -c "! printf '%s' \"\$0\" | grep -q AFTER" "$out"
check "nothing was deleted" [ -f "$FAKE/Documents/f1" ]
c=$(code); run "ack $c && rm -rf \"$FAKE\" && echo gone"
check "an ack for that exact effect lets it through" bash -c "[ ! -e '$FAKE' ]"
mkdir -p "$FAKE" && for i in $(seq 60); do echo $i > "$FAKE/g$i"; done
cat > "$WS/t.sh" <<EOF
D=\$(mktemp -d); trap 'rm -rf "\$D"' EXIT
D="$FAKE"      # a later line repoints the variable; the trap reads it at exit
EOF
run 'bash t.sh'
check "a deferred trap inside a script is seen with the value it has at exit" stopped
check "the tree it was about to delete is intact" [ -f "$FAKE/g1" ]
STORE=$LAB/store; for l in lane1 lane2 lane7; do mkdir -p $STORE/$l; for i in $(seq 25); do echo $i > $STORE/$l/o$i; done; done
run "LANE=; rm -rf \"$STORE/\$LANE\"* 2>/dev/null"
check "a glob that widened to every lane names the lanes" says 'lane1, lane2, lane7'
run "find $STORE -name 'o*' -delete"
check "find -delete is judged on what it would match" stopped
rm -f "$WS/t.sh"

echo "ownership and modes that reach further than the path (fleet F4, F5; claude-code#39283)"
run 'chmod 644 /dev/null; echo AFTER'
check "chmod on a device node is stopped" says 'device node'
mkdir -p $LAB/tmpl $LAB/slot && for i in 1 2 3; do echo $i > $LAB/tmpl/t$i && ln $LAB/tmpl/t$i $LAB/slot/t$i; done
run "chmod -R g+w $LAB/slot"
check "recursive change over hard links says where else it lands" says 'hard links'
run 'touch s.sh && chmod +x s.sh'
check "chmod +x on a workspace file is nobody's business" bash -c "! printf '%s' \"\$0\" | grep -q STOPPED" "$out"
rm -f "$WS/s.sh"

echo "signals to processes the agent did not start (fleet 11-21, BSD pkill)"
# The victim's pid comes from $!, never from a pattern search: a pattern also
# matches any shell whose command line happens to mention it, which is the
# very mistake this section is about (and one made while writing this file).
env -u MEANT_SESSION sleep 43217 >/dev/null 2>&1 &
victim=$!; disown 2>/dev/null; sleep 0.3
if command -v pgrep >/dev/null; then
  run "ps -eo pid,args | awk '/sleep 4321[7]/ {print \$1}' | xargs kill; echo AFTER"
else  # Git Bash has neither pgrep nor ps -o; the pid reaches kill through xargs all the same
  run "echo $victim | xargs kill; echo AFTER"
fi
check "ps | awk | xargs kill of a foreign process is stopped" stopped
check "the process is still alive" kill -0 "$victim"
run "kill $victim"
check "the kill builtin is seen too" stopped
if command -v pkill >/dev/null; then
  run 'pkill -f "sleep 4321[7]"'
  check "pkill is judged on what the pattern matches now" stopped
else
  printf '  skip  no pkill here\n'
fi
kill "$victim" 2>/dev/null
run 'sleep 30771 >/dev/null 2>&1 & echo "started $!"'
check "a process left running is reported" says 'still running: sleep'
own=$(printf '%s' "$out" | sed -n 's/^started //p')
run "kill $own && echo killed"
check "killing a process this session started passes" says killed

echo "uncommitted work destroyed (claude-code#7232, #34327)"
cd "$WS" && for f in w1 w2 w3; do echo "hours of work" > $f.py; done; echo edit >> a.txt
run 'git reset -q --hard && git clean -fdq'
check "reset --hard + clean over uncommitted work is flagged" says 'lost content that was never committed'
c=$(printf '%s' "$out" | grep -o 'reject [0-9a-f]\{4\}' | head -1 | cut -d' ' -f2)
run "reject $c"
check "reject puts the work back" bash -c "[ -f '$WS/w1.py' ] && grep -q edit '$WS/a.txt'"
run 'git stash -q -u && git stash pop -q'
check "a stash is not a loss" bash -c "! printf '%s' \"\$0\" | grep -q 'lost content'" "$out"
run "$G add -A && $G commit -qm wip && git checkout -q -b other HEAD~1 && git checkout -q main"
check "switching branches over committed work is not a loss" bash -c "! printf '%s' \"\$0\" | grep -q 'lost content'" "$out"

echo "history thrown away (claude-code#33402; fleet F6)"
$G init -q --bare $LAB/remote.git && cd "$WS" && git remote add origin $LAB/remote.git && git push -q origin main
git reset -q --hard HEAD~1 && echo other > c.txt && $G add -A && $G commit -qm rewrite
run 'git push -q --force origin main; echo AFTER'
check "a forced push that discards remote commits is stopped" says 'discarding 1 commit'
check "the remote still has them" [ "$(git -C $LAB/remote.git rev-list --count main)" = 2 ]
run 'git pull -q --rebase origin main 2>/dev/null; git push -q origin main && echo pushed'
check "an ordinary push passes" says pushed
git checkout -q -b side && echo side >> a.txt && $G commit -qam side && git checkout -q main &&
  echo mainline >> a.txt && $G commit -qam mainline && git checkout -q side && $G rebase main >/dev/null 2>&1
run "git add -A && GIT_EDITOR=true $G rebase --continue"
check "committing files that still hold conflict markers is stopped" says 'conflict markers in 1 file'
git rebase --abort 2>/dev/null; git checkout -q main

echo "effects out of sight (claude-code#27063)"
run 'drizzle-kit push --force'
check "a declared remote-destructive command asks first" stopped
SSHX='ssh -o BatchMode=yes -o ConnectTimeout=1 host.invalid "rm -rf /x" 2>/dev/null'
run "$SSHX; echo AFTER"
check "a command sent to a host nobody decided about stops" stopped
check "and says how to decide" says 'meant remote allow host.invalid'
c=$(code); run "ack $c && $SSHX; echo AFTER"
check "an ack lets this one call through" says AFTER
"$MEANT" remote allow host.invalid >/dev/null
run "$SSHX; echo AFTER"
check "an allowed host runs unobserved, and says so" says 'nothing it did there was observed'
"$MEANT" remote remove host.invalid >/dev/null

echo "the same mistakes on another machine, reached over ssh (6 of 8 fleet incidents)"
if ssh -o BatchMode=yes -o ConnectTimeout=5 localhost true 2>/dev/null; then
  # "Another machine" here is this one through sshd, with its own copy of
  # meant and its own state, which is all the mechanism can tell apart.
  "$MEANT" remote add --path "$LAB/far/meant" --state "$LAB/far/state" -- -o BatchMode=yes localhost >/dev/null
  FAR=$LAB/far-store; for l in lane1 lane2; do mkdir -p $FAR/$l; for i in $(seq 30); do echo $i > $FAR/$l/o$i; done; done
  run "ssh localhost 'echo hi; exit 3'"
  check "a remote command's output and exit status pass through" [ "$rc" = 3 -a "$out" = hi ]
  run "ssh localhost 'rm -rf $FAR/* 2>/dev/null; echo FAR-AFTER' && echo NEAR-AFTER; echo NEAR-AFTER2"
  check "a delete on the far side is judged on the far side" says 'lane1, lane2'
  check "the far command stops there" bash -c "! printf '%s' \"\$0\" | grep -q FAR-AFTER" "$out"
  check "and the near command stops with it" bash -c "! printf '%s' \"\$0\" | grep -q NEAR-AFTER" "$out"
  check "nothing was deleted over there" [ -f "$FAR/lane1/o1" ]
  c=$(code); run "ack $c && ssh localhost 'rm -rf $FAR/*' && echo done"
  check "an ack given here is honoured there" bash -c "[ ! -e '$FAR/lane1' ]"
  run "ssh localhost 'bash -s' <<'EOS'
chmod 644 /dev/null
echo FAR-AFTER
EOS"
  check "a script fed over stdin is watched like any other" says 'device node'
  env -u MEANT_SESSION sleep 43218 >/dev/null 2>&1 & far_victim=$!; disown 2>/dev/null
  run "ssh localhost 'kill $far_victim'"
  check "a far-side kill of a process this session did not start is stopped" stopped
  kill "$far_victim" 2>/dev/null
  run "ssh -O check localhost 2>/dev/null; ssh -G localhost | head -1"
  check "ssh used for anything but a remote command is left alone" bash -c "! printf '%s' \"\$0\" | grep -q STOPPED" "$out"
else
  printf '  skip  no sshd on localhost to stand in for another machine\n'
fi

echo "robustness"
out=$(PATH="$MEANT_HOME/shims:$PATH" git --version 2>&1); rc=$?
check "a shim reached outside any meant command just runs the real program" [ "$rc" = 0 ]
run 'export PATH=/usr/bin:/bin; type rm'
check "shims survive a command that resets PATH" says "$MEANT_HOME/shims/rm"

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" = 0 ]
