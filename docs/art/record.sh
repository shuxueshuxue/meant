#!/bin/sh
# Records the demo for real: every command below runs under the meant built
# in bin/, and its exact output is what the animation shows.
# usage: docs/art/record.sh > docs/art/demo.json
set -e
root=$(cd "$(dirname "$0")/../.." && pwd)
lab=$(mktemp -d)
store=/dev/shm/store            # outside the workspace and the temp dirs, and short to print
[ -e "$store" ] && { echo "$store exists; not touching it" >&2; exit 1; }
mkdir -p "$lab/proj" "$lab/state" "$store"
trap 'rm -rf "$lab" "$store"' EXIT
for l in lane1 lane2 lane7; do mkdir -p $store/$l; for i in $(seq 30); do echo $i > $store/$l/obj$i.bin; done; done
cd "$lab/proj"
git init -q && : > .lane && git add .lane && git -c user.name=demo -c user.email=demo@localhost commit -qm init
export MEANT_HOME="$lab/state" STORE="$store" MEANT_SESSION=demo
python3 -I - "$root/bin/meant" <<'PY'
import json, subprocess, sys
steps = [
    'ls $STORE',
    'LANE=$(cat .lane); rm -rf "$STORE/$LANE"* && echo cleaned',
    'wc -c < .lane',
    'echo lane7 > .lane; LANE=$(cat .lane); rm -rf "$STORE/$LANE"* && echo cleaned',
    'ls $STORE',
]
out = []
for c in steps:
    p = subprocess.run([sys.argv[1], "run", "-c", c], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    out.append({"cmd": c, "out": p.stdout, "exit": p.returncode})
print(json.dumps(out, indent=1))
PY
