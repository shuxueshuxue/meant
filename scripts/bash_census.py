#!/usr/bin/env python3
"""Count how agents actually use the shell, from Claude Code session transcripts.

Reads tool_use blocks out of session .jsonl files and sorts every Bash call
by how much of what it does is visible in the command text. Prints counts
only; no command text leaves this script except a few short samples per
bucket when --samples is given.

  python3 -I scripts/bash_census.py <projects dir> [--since YYYY-MM-DD] [--samples N]
"""
import collections
import datetime
import json
import os
import re
import sys

root = sys.argv[1]
since = None
samples = 0
for i, a in enumerate(sys.argv):
    if a == "--since":
        since = datetime.datetime.fromisoformat(sys.argv[i + 1]).timestamp()
    if a == "--samples":
        samples = int(sys.argv[i + 1])

# A call is split into statements and each statement is judged by the program
# in command position, after env assignments and wrappers like `timeout 60`.
# The call's bucket is the most opaque thing any of its statements does.
INTERP = {"python", "python3", "node", "bun", "deno", "perl", "ruby", "php", "tsx", "ts-node"}
SHELLS = {"bash", "sh", "zsh"}
RUNNERS = {"npm", "pnpm", "yarn", "npx", "make", "cargo", "go", "pytest", "vitest", "jest", "tsc", "eslint",
           "prettier", "playwright", "turbo", "gradle", "mvn"}
WRAPPERS = {"sudo", "timeout", "nohup", "time", "env", "exec", "command", "xargs", "nice", "setsid", "stdbuf"}
SCRIPT_EXT = re.compile(r"\.(?:sh|bash|py|m?js|cjs|ts|pl|rb|php)$")
ASSIGN = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*=")
ORDER = ["plain commands", "build/test runner", "script file", "inline code"]


def judge(stmt, piped_into):
    words = stmt.replace("(", " ").replace("{", " ").split()
    while words and (ASSIGN.match(words[0]) or words[0] in WRAPPERS or re.fullmatch(r"-\S*|\d+[smh]?", words[0])):
        words.pop(0)
    if not words:
        return 0
    head = words[0].strip("'\"`$")
    base = head.rsplit("/", 1)[-1]
    base = re.sub(r"\d+(\.\d+)*$", "", base) if base.startswith("python") else base
    args = words[1:]
    if base in INTERP or base in SHELLS:
        first = next((a for a in args if not a.startswith("-")), "")
        flags = [a for a in args if a.startswith("-")]
        if base in SHELLS and ("-c" in flags or "-lc" in flags):
            return 0  # a nested shell: its text is judged as statements of its own
        if "<<" in stmt or piped_into or any(f in ("-c", "-e", "--eval", "-p", "-", "-s") for f in flags):
            return 3
        if first:
            return 2
        return 3 if piped_into else 0
    if head.startswith(("./", "../")) or (head.startswith(("/", "~/")) and SCRIPT_EXT.search(head)):
        return 2
    if base in ("source", ".") and args:
        return 2
    if base in RUNNERS:
        return 1
    return 0


def bucket(cmd):
    worst = 0
    for part in re.split(r"&&|\|\||;|\n", cmd):
        segs = part.split("|")
        for i, seg in enumerate(segs):
            worst = max(worst, judge(seg, i > 0))
    return ORDER[worst]


SSH = re.compile(r"(?:^|[\s;&|(])(?:ssh|scp|rsync)\s")
HEREDOC = re.compile(r"<<-?\s*['\"]?\w+")
IRREV = re.compile(r"(?:^|[\s;&|(`$])(?:sudo\s+)?(?:rm|kill|pkill|killall|chown|chmod|chgrp)\s|\bgit\s+(?:push|reset\s+--hard|clean)\b|\bfind\b[^\n|;]*-delete\b")
SPLIT = re.compile(r"&&|\|\||;|\n|\|")


tools = collections.Counter()
buckets = collections.Counter()
remote = collections.Counter()
shown = collections.defaultdict(list)
stmts = []
lengths = []
heredocs = irrev = irrev_plain = irrev_local = 0
seen = set()
files = sessions = 0

for dirpath, _, names in os.walk(root):
    for n in names:
        if not n.endswith(".jsonl"):
            continue
        path = os.path.join(dirpath, n)
        if since and os.path.getmtime(path) < since:
            continue
        files += 1
        used = False
        with open(path, errors="replace") as f:
            for line in f:
                if '"tool_use"' not in line:
                    continue
                try:
                    ev = json.loads(line)
                except ValueError:
                    continue
                msg = ev.get("message")
                if not isinstance(msg, dict) or not isinstance(msg.get("content"), list):
                    continue
                for c in msg["content"]:
                    if not isinstance(c, dict) or c.get("type") != "tool_use" or c.get("id") in seen:
                        continue
                    seen.add(c.get("id"))
                    used = True
                    tools[c.get("name", "?")] += 1
                    if c.get("name") != "Bash":
                        continue
                    cmd = (c.get("input") or {}).get("command") or ""
                    b = bucket(cmd)
                    buckets[b] += 1
                    is_remote = bool(SSH.search(cmd))
                    remote[(b, is_remote)] += 1
                    stmts.append(len([s for s in SPLIT.split(cmd) if s.strip()]))
                    lengths.append(len(cmd))
                    heredocs += bool(HEREDOC.search(cmd))
                    if IRREV.search(cmd):
                        irrev += 1
                        irrev_plain += b == "plain commands" and not is_remote
                        irrev_local += not is_remote
                    if samples and len(shown[b]) < samples and len(cmd) < 160:
                        shown[b].append(cmd.replace("\n", " ⏎ "))
        sessions += used

total = sum(buckets.values())
if not total:
    sys.exit("no Bash calls found")


def pct(n, d=total):
    return f"{n:>8,}  {100 * n / d:5.1f}%"


def q(xs, p):
    xs = sorted(xs)
    return xs[min(len(xs) - 1, int(p * len(xs)))]


print(f"{files} transcript files, {sessions} with tool calls, {sum(tools.values()):,} tool calls")
print("\ntool mix")
for name, n in tools.most_common(8):
    print(f"  {name:<22}{pct(n, sum(tools.values()))}")
print(f"\nBash calls: {total:,}, by the most opaque thing each one does")
for name in ["plain commands", "build/test runner", "script file", "inline code"]:
    n = buckets[name]
    r = remote[(name, True)]
    print(f"  {name:<22}{pct(n)}   of which over ssh/scp/rsync: {r:,}")
r_all = sum(v for (b, rem), v in remote.items() if rem)
print(f"\n  any ssh/scp/rsync      {pct(r_all)}")
print(f"  uses a heredoc         {pct(heredocs)}")
print(f"  names rm/kill/chmod/chown/git push|reset --hard|clean/find -delete in its text: {pct(irrev)}")
print(f"    ... and runs locally (no ssh): {pct(irrev_local)}")
print(f"    ... and is a plain local command: {pct(irrev_plain)}")
print(f"\nstatements per call (split on && || ; | newline): median {q(stmts, .5)}, p90 {q(stmts, .9)}, p99 {q(stmts, .99)}")
print(f"characters per call: median {q(lengths, .5)}, p90 {q(lengths, .9)}, p99 {q(lengths, .99)}")
for b, xs in shown.items():
    print(f"\nsamples: {b}")
    for x in xs:
        print("   ", x)
