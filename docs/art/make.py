"""Draws docs/header.svg and docs/demo.svg.

    python3 -I docs/art/make.py

The look is a hand sketch: strokes wobble a little, fills are pencil hatching,
labels are handwritten. The randomness is seeded, so every run draws the same
picture. The demo replays docs/art/demo.json, which record.sh fills with what
meant really printed.
"""
import base64, json, math, os, random

HERE = os.path.dirname(os.path.abspath(__file__))
DOCS = os.path.dirname(HERE)

INK, SOFT = "#1e1e1e", "#868e96"
BLUE, BLUE_F = "#1971c2", "#a5d8ff"
PINK, PINK_F = "#c2255c", "#fcc2d7"
GREEN, GREEN_F = "#2f9e44", "#b2f2bb"
YELLOW_F, GREY_F = "#ffec99", "#e9ecef"
RED = "#e03131"
PAPER = "#fdfcf8"
MONO_ADVANCE = 0.6  # Cascadia Code, in ems


def font_face(name, file):
    data = base64.b64encode(open(os.path.join(HERE, "fonts", file), "rb").read()).decode()
    return f"@font-face{{font-family:'{name}';src:url(data:font/woff2;base64,{data}) format('woff2');}}"


FONTS = font_face("Virgil", "Virgil.woff2") + font_face("Cascadia", "Cascadia.woff2")
CLASSES = ".h{font-family:Virgil,cursive;fill:%s}.m{font-family:Cascadia,monospace;fill:%s;white-space:pre}" % (INK, INK)


def esc(s):
    return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


class Sketch:
    def __init__(self, w, h, seed):
        self.w, self.h, self.items, self.css = w, h, [], []
        self.r = random.Random(seed)

    def j(self, amt):
        return self.r.uniform(-amt, amt)

    def add(self, el, cls=None):
        if cls:
            el = el.replace("<", f'<g class="{cls}"><', 1) + "</g>"
        self.items.append(el)

    def line(self, x1, y1, x2, y2, color=INK, width=2.2, rough=1.2, passes=2, cls=None):
        out = ""
        for _ in range(passes):
            bow = min(math.hypot(x2 - x1, y2 - y1) * 0.012, 3) * rough
            mx, my = (x1 + x2) / 2 + self.j(bow), (y1 + y2) / 2 + self.j(bow)
            out += (f'<path d="M{x1 + self.j(rough):.1f},{y1 + self.j(rough):.1f} Q{mx:.1f},{my:.1f} '
                    f'{x2 + self.j(rough):.1f},{y2 + self.j(rough):.1f}" fill="none" stroke="{color}" '
                    f'stroke-width="{width}" stroke-linecap="round"/>')
        self.add(out, cls)

    def rect(self, x, y, w, h, **k):
        o = 4  # a hand does not stop exactly at the corner
        self.line(x - o, y, x + w + self.j(o), y, **k)
        self.line(x + w, y - o, x + w, y + h + self.j(o), **k)
        self.line(x + w + o, y + h, x + self.j(o), y + h, **k)
        self.line(x, y + h + o, x, y + self.j(o), **k)

    def curve(self, pts, color=INK, width=2.2, cls=None):
        d = f"M{pts[0][0]:.1f},{pts[0][1]:.1f} " + " ".join(
            f"Q{c[0]:.1f},{c[1]:.1f} {e[0]:.1f},{e[1]:.1f}" for c, e in zip(pts[1::2], pts[2::2]))
        self.add(f'<path d="{d}" fill="none" stroke="{color}" stroke-width="{width}" stroke-linecap="round"/>', cls)

    def arrow(self, x1, y1, cx, cy, x2, y2, color=INK, cls=None):
        for _ in range(2):
            self.curve([(x1 + self.j(1), y1 + self.j(1)), (cx + self.j(3), cy + self.j(3)), (x2, y2)], color, cls=cls)
        ang = math.atan2(y2 - cy, x2 - cx)
        for s in (-1, 1):
            a = ang + math.pi - s * 0.45
            self.line(x2, y2, x2 + 13 * math.cos(a), y2 + 13 * math.sin(a), color=color, passes=1, cls=cls)

    def circle(self, cx, cy, r, color=INK, fill="none", cls=None):
        out = ""
        for _ in range(2):
            start = self.r.uniform(0, 6.28)
            pts = [(cx + (r + self.j(1.2)) * math.cos(start + i / 18 * 2.08 * math.pi),
                    cy + (r + self.j(1.2)) * math.sin(start + i / 18 * 2.08 * math.pi)) for i in range(19)]
            out += (f'<path d="M' + " L".join(f"{a:.1f},{b:.1f}" for a, b in pts) +
                    f'" fill="{fill}" stroke="{color}" stroke-width="1.8" stroke-linejoin="round"/>')
            fill = "none"
        self.add(out, cls)

    def hatch(self, pts, color, gap=7, slope=1.15):
        """Pencil hatching clipped to a slightly irregular outline."""
        cid = f"c{len(self.items)}"
        poly = " ".join(f"{a + self.j(2):.1f},{b + self.j(2):.1f}" for a, b in pts)
        xs, ys = [p[0] for p in pts], [p[1] for p in pts]
        x0, x1, y0, y1 = min(xs), max(xs), min(ys) - 4, max(ys) + 4
        rise = (y1 - y0) / slope
        strokes, k = [], x0 - rise
        while k < x1:
            strokes.append(f'<path d="M{k + self.j(1):.1f},{y1:.1f} L{k + rise + self.j(1):.1f},{y0:.1f}" '
                           f'stroke="{color}" stroke-width="2.2" stroke-linecap="round"/>')
            k += gap + self.j(1)
        self.add(f'<clipPath id="{cid}"><polygon points="{poly}"/></clipPath>'
                 f'<g clip-path="url(#{cid})">' + "".join(strokes) + "</g>")

    def box(self, x, y, w, h, fill, color=INK):
        self.hatch([(x, y), (x + w, y), (x + w, y + h), (x, y + h)], fill)
        self.rect(x, y, w, h, color=color)

    def text(self, x, y, s, size=22, font="h", color=None, anchor="start", rot=0, cls=None, ident=None):
        fill = f' style="fill:{color}"' if color else ""
        tr = f' transform="rotate({rot} {x} {y})"' if rot else ""
        idattr = f' id="{ident}"' if ident else ""
        self.add(f'<text{idattr} x="{x:.1f}" y="{y:.1f}" class="{font}" font-size="{size}" '
                 f'text-anchor="{anchor}"{fill}{tr}>{esc(s)}</text>', cls)

    def svg(self):
        # MEANT_ART_AT=<seconds> freezes the animation there, for checking frames.
        if os.environ.get("MEANT_ART_AT"):
            self.css.append("*{animation-play-state:paused!important;animation-delay:-%ss!important}" % os.environ["MEANT_ART_AT"])
        return (f'<svg xmlns="http://www.w3.org/2000/svg" width="{self.w}" height="{self.h}" '
                f'viewBox="0 0 {self.w} {self.h}"><style>{FONTS}{CLASSES}{"".join(self.css)}</style>'
                f'<rect width="100%" height="100%" fill="{PAPER}"/>' + "".join(self.items) + "</svg>")


CAT = [" /\\_/\\ ", "( o.o )", " > ^ < "]


def header():
    s = Sketch(1280, 640, 11)
    s.text(96, 128, "meant", 68, "m")
    s.text(100, 172, "did you mean it?", 30, color=BLUE)

    top, mono = 300, 22
    s.text(90, top - 30, "what you meant", 24, color=BLUE)
    s.text(90, top + 8, 'rm -rf "$STORE/$LANE"*', mono, "m")
    s.text(90, top + 118, "what it did", 24, color=PINK)
    s.text(90, top + 158, "rm -rf lane1 ", mono, "m")
    s.text(90 + 13 * mono * MONO_ADVANCE, top + 158, "lane2 lane7", mono, "m", color=RED)
    s.line(420, top, 560, top + 34, color=BLUE, width=3)
    s.line(420, top + 150, 560, top + 114, color=PINK, width=3)

    # The gate: a flat back and a round front, with the bubble of a NAND.
    gx, gy, gh = 560, top - 6, 160
    front = [(gx + 70 + 44 * math.sin(math.pi * i / 12), gy + gh * i / 12) for i in range(13)]
    s.hatch([(gx, gy), (gx + 70, gy)] + front + [(gx, gy + gh)], YELLOW_F)
    s.line(gx, gy, gx, gy + gh)
    s.line(gx, gy, gx + 70, gy)
    s.line(gx, gy + gh, gx + 70, gy + gh)
    for _ in range(2):
        s.curve([(gx + 70, gy + s.j(1)), (gx + 160 + s.j(3), gy + gh / 2), (gx + 70, gy + gh + s.j(1))])
    s.circle(gx + 125, gy + gh / 2, 8)
    s.text(gx + 50, gy + gh / 2 + 12, "≟", 40, anchor="middle")

    s.arrow(gx + 135, gy + gh / 2, gx + 190, gy + gh / 2 - 6, gx + 236, gy + gh / 2, color=RED)
    tx, ty = 800, top - 10
    s.box(tx, ty, 400, 150, GREY_F)
    s.text(tx + 22, ty + 44, "meant: STOPPED", 20, "m", color=RED)
    for i, l in enumerate(["  would delete 3 lanes", "", "$ ack 3f2a && …"]):
        s.text(tx + 22, ty + 69 + i * 25, l, 20, "m")
    for i, l in enumerate(CAT):
        s.text(tx + 300, ty - 64 + i * 22, l, 18, "m", color=BLUE)

    s.text(1184, 590, "a second look, at the moment of exec", 22, color=SOFT, anchor="end")
    return s.svg()


def demo():
    """The terminal replays a real recording; only the typing speed is made up."""
    steps = json.load(open(os.path.join(HERE, "demo.json")))
    size, lh, pad = 15, 22, 26
    cw = size * MONO_ADVANCE
    lines = []  # (kind, text, start time)
    t = 0.8
    for st in steps:
        cmd = st["cmd"]
        lines.append(("cmd", cmd, t))
        t += 0.25 + 0.035 * len(cmd) + 0.35
        out = st["out"].rstrip("\n").split("\n") if st["out"].strip() else []
        for l in out:
            lines.append(("out", l, t))
        t += 0.5 + (2.8 if any(l.startswith("meant: STOPPED") for l in out) else 0.6)
    lines.append(("prompt", "", t))
    total = t + 4.0

    width = 2 * pad + 40 + max(len(l[1]) + 2 for l in lines) * cw
    height = 2 * pad + 46 + len(lines) * lh + 56
    s = Sketch(int(width), int(height), 5)
    pct = lambda x: f"{100 * x / total:.3f}%"
    n = [0]

    def show_at(when, extra=""):
        n[0] += 1
        name = f"k{n[0]}"
        s.css.append(f"@keyframes {name}{{0%,{pct(when)}{{opacity:0}}{pct(when + .01)},97%{{opacity:1}}100%{{opacity:0}}}}"
                     f".{name}{{opacity:0;animation:{name} {total:.2f}s step-end infinite}}{extra}")
        return name

    x0, y0 = 20, 20
    s.box(x0, y0, width - 40, 40, GREY_F)
    for i, c in enumerate((PINK_F, YELLOW_F, GREEN_F)):
        s.circle(x0 + 26 + i * 24, y0 + 20, 6, fill=c)
    s.text(width / 2, y0 + 27, "an agent's shell, under meant", 18, color=SOFT, anchor="middle")
    s.rect(x0, y0, width - 40, height - 40 - 50)

    y = y0 + 40 + pad + 4
    stopped_y = None
    for kind, text, when in lines:
        x = x0 + pad
        if kind in ("cmd", "prompt"):
            s.text(x, y, "$", size, "m", color=BLUE, cls=show_at(when))
            for i, ch in enumerate(text):
                if ch != " ":
                    s.text(x + (i + 2) * cw, y, ch, size, "m", cls=show_at(when + 0.25 + 0.035 * i))
            if kind == "prompt":
                blink = show_at(when, "")
                s.css.append(f"@keyframes bl{{50%{{fill-opacity:0}}}}.cur rect{{animation:bl 1s step-end infinite}}")
                s.add(f'<g class="{blink}"><g class="cur"><rect x="{x + 2 * cw:.1f}" y="{y - size + 3}" '
                      f'width="{cw:.1f}" height="{size + 2}" fill="{INK}"/></g></g>')
        else:
            color = None
            if text.startswith("meant: STOPPED"):
                color, stopped_y = RED, y
            elif text.lstrip().startswith("!"):
                color = PINK
            elif text.startswith("meant:") or text.startswith("  "):
                color = "#495057"
            s.text(x, y, text, size, "m", color=color, cls=show_at(when))
        y += lh

    # One note in the margin, in the hand of whoever reads the transcript later.
    when = next(w for k, t_, w in lines if t_.startswith("meant: STOPPED")) + 1.2
    note = show_at(when)
    tip_x, tip_y = x0 + pad + 71 * cw + 6, stopped_y + lh - 6  # the end of the expanded rm line
    nx, ny = tip_x + 56, stopped_y - 26
    s.text(nx, ny, "LANE was empty", 22, color=RED, rot=-2, cls=note)
    s.arrow(nx + 10, ny + 10, nx - 8, ny + 30, tip_x + 8, tip_y - 4, color=RED, cls=note)
    s.text(width - 30, height - 18, "real output; only the typing is slowed down", 16, color=SOFT, anchor="end")
    return s.svg()


def playground():
    """A toy of one rule, to play with: the gate from the header, live."""
    s = Sketch(1100, 430, 23)
    top, mono = 120, 20
    s.text(60, top - 30, "what you meant", 22, color=BLUE)
    s.text(60, top + 6, 'rm -rf "$STORE/$LANE"*', mono, "m")
    s.text(60, top + 120, "what it did", 22, color=PINK)
    s.text(60, top + 156, "", mono, "m", ident="did")
    s.line(350, top, 470, top + 34, color=BLUE, width=3)
    s.line(350, top + 150, 470, top + 114, color=PINK, width=3)
    gx, gy, gh = 470, top - 6, 160
    front = [(gx + 70 + 44 * math.sin(math.pi * i / 12), gy + gh * i / 12) for i in range(13)]
    s.hatch([(gx, gy), (gx + 70, gy)] + front + [(gx, gy + gh)], YELLOW_F)
    s.line(gx, gy, gx, gy + gh)
    s.line(gx, gy, gx + 70, gy)
    s.line(gx, gy + gh, gx + 70, gy + gh)
    for _ in range(2):
        s.curve([(gx + 70, gy + s.j(1)), (gx + 160 + s.j(3), gy + gh / 2), (gx + 70, gy + gh + s.j(1))])
    s.circle(gx + 125, gy + gh / 2, 8)
    s.text(gx + 50, gy + gh / 2 + 12, "≟", 40, anchor="middle")
    s.add('<g id="wire">')
    s.arrow(gx + 135, gy + gh / 2, gx + 175, gy + gh / 2 - 6, gx + 215, gy + gh / 2, color=INK)
    s.add('</g>')
    tx, ty = 690, top - 20
    s.box(tx, ty, 380, 200, GREY_F)
    for i in range(6):
        s.text(tx + 18, ty + 36 + i * 26, "", 16, "m", ident=f"t{i}")
    svg = s.svg().replace(f'<rect width="100%" height="100%" fill="{PAPER}"/>', "")
    return """<!doctype html>
<meta charset="utf-8">
<title>meant · the gate</title>
<style>
body{background:%(paper)s;color:%(ink)s;font-family:Virgil,cursive;max-width:1100px;margin:48px auto;padding:0 24px}
label,input,small{font-family:Cascadia,monospace;font-size:20px}
input{background:transparent;border:0;border-bottom:2px solid %(ink)s;width:9ch;font-size:20px;outline:0}
.row{margin:0 0 8px 60px} small{font-size:14px;color:%(soft)s;font-family:Virgil,cursive}
p.h{font-size:22px;margin:8px 0 28px}
</style>
<p class="h">meant sees the command after the shell has expanded it. Change <code>LANE</code>; the gate gets the real argv.</p>
<div class="row"><label>STORE=/srv/store &nbsp; LANE=<input id="lane" value="" spellcheck="false" autofocus></label>
<label style="margin-left:2em"><input type="checkbox" id="inws" style="width:auto"> the store is in the workspace</label></div>
%(svg)s
<p class="h" style="margin-top:12px"><small>A toy model of one rule (deletes outside the workspace stop past 50 entries; /srv/store holds lane1, lane2, lane7, 30 files each). The real thing is <code>meant</code>, not this page.</small></p>
<script>
const lanes = ["lane1", "lane2", "lane7"], per = 30, limit = 50;
const $ = id => document.getElementById(id);
function update() {
  const lane = $("lane").value, inws = $("inws").checked;
  const hit = lanes.filter(l => l.startsWith(lane));
  // An unmatched glob is passed on as written; rm -f is then silent.
  const argv = hit.length ? hit.map(l => "/srv/store/" + l) : ["/srv/store/" + lane + "*"];
  $("did").textContent = "rm -rf " + (hit.length ? hit.join(" ") : "/srv/store/" + lane + "*");
  const files = hit.length * per, entries = files + hit.length;
  let out;
  if (!hit.length) out = ["$ rm -rf …", "(nothing matched; rm -f says nothing)"];
  else if (inws || entries <= limit) out = ["meant: deleted " + files + " files in " + hit.length +
      (hit.length > 1 ? " dirs" : " dir"), inws ? "  (in the workspace: snapshotted," : "", inws ? "   `meant undo` brings it back)" : ""];
  else out = ["meant: STOPPED", "  rm -rf " + argv.map(a => a.split("/").pop()).join(" "),
      "  would delete " + files + " files", "  ! outside your workspace", "", "$ ack <code> && …   or fix it"];
  const stop = out[0] === "meant: STOPPED";
  for (let i = 0; i < 6; i++) { const t = $("t" + i); t.textContent = out[i] || ""; t.style.fill = i === 0 && stop ? "%(red)s" : ""; }
  $("wire").querySelectorAll("path").forEach(p => p.setAttribute("stroke", stop ? "%(red)s" : "%(green)s"));
}
$("lane").addEventListener("input", update); $("inws").addEventListener("change", update); update();
</script>
""" % {"paper": PAPER, "ink": INK, "soft": SOFT, "red": RED, "green": GREEN, "svg": svg}


if __name__ == "__main__":
    open(os.path.join(DOCS, "header.svg"), "w").write(header())
    open(os.path.join(DOCS, "demo.svg"), "w").write(demo())
    open(os.path.join(DOCS, "playground.html"), "w").write(playground())
    print("wrote docs/header.svg docs/demo.svg docs/playground.html")
