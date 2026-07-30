#!/usr/bin/env python3
"""Render a terminal capture (ANSI SGR) as a self-contained SVG.

Written for agentdeck's README: the screenshot is the real TUI, captured from a
real tmux client, rather than a mock-up. Each styled run becomes one <tspan>
pinned to its column with textLength, so glyph-width differences between the
viewer's monospace font and the capture never accumulate into drift.
"""
import re
import sys
import unicodedata
from html import escape

CELL_W = 8.4
CELL_H = 18.0
PAD = 20.0
FONT_SIZE = 14.0

# xterm 256-colour palette, and a background tuned to read well on GitHub in
# both light and dark page themes.
BG = "#12141c"
FG = "#c8ccd6"

BASE16 = [
    "#1a1c25", "#e05561", "#8cc265", "#d18f52", "#4aa5f0", "#c162de", "#42b3c2", "#c8ccd6",
    "#4a4f5e", "#ff616e", "#a5e075", "#f0a45d", "#4dc4ff", "#de73ff", "#4cd1e0", "#e6e6e6",
]


def xterm256(n):
    if n < 16:
        return BASE16[n]
    if n < 232:
        n -= 16
        r, g, b = n // 36, (n % 36) // 6, n % 6
        lv = [0, 95, 135, 175, 215, 255]
        return "#%02x%02x%02x" % (lv[r], lv[g], lv[b])
    v = 8 + (n - 232) * 10
    return "#%02x%02x%02x" % (v, v, v)


class Style:
    __slots__ = ("fg", "bg", "bold", "dim", "italic", "underline", "reverse")

    def __init__(self):
        self.reset()

    def reset(self):
        self.fg = None
        self.bg = None
        self.bold = False
        self.dim = False
        self.italic = False
        self.underline = False
        self.reverse = False

    def copy(self):
        s = Style()
        for k in self.__slots__:
            setattr(s, k, getattr(self, k))
        return s

    def key(self):
        return tuple(getattr(self, k) for k in self.__slots__)


def apply_sgr(st, params):
    i = 0
    while i < len(params):
        p = params[i]
        if p in (0, None):
            st.reset()
        elif p == 1:
            st.bold = True
        elif p == 2:
            st.dim = True
        elif p == 3:
            st.italic = True
        elif p == 4:
            st.underline = True
        elif p == 7:
            st.reverse = True
        elif p == 22:
            st.bold = st.dim = False
        elif p == 23:
            st.italic = False
        elif p == 24:
            st.underline = False
        elif p == 27:
            st.reverse = False
        elif 30 <= p <= 37:
            st.fg = BASE16[p - 30]
        elif p == 39:
            st.fg = None
        elif 40 <= p <= 47:
            st.bg = BASE16[p - 40]
        elif p == 49:
            st.bg = None
        elif 90 <= p <= 97:
            st.fg = BASE16[p - 90 + 8]
        elif 100 <= p <= 107:
            st.bg = BASE16[p - 100 + 8]
        elif p in (38, 48):
            target = "fg" if p == 38 else "bg"
            if i + 1 < len(params) and params[i + 1] == 5:
                setattr(st, target, xterm256(params[i + 2]))
                i += 2
            elif i + 1 < len(params) and params[i + 1] == 2:
                r, g, b = params[i + 2], params[i + 3], params[i + 4]
                setattr(st, target, "#%02x%02x%02x" % (r, g, b))
                i += 4
        i += 1


def cell_width(ch):
    """Terminal cells one character occupies.

    Status glyphs and box drawing are one cell, but several of the characters a
    TUI reaches for are East Asian Wide and take two. Counting characters
    instead of cells shifts every run after one of them to the left, and makes
    the canvas too narrow — which crops the right-hand column.
    """
    if unicodedata.combining(ch):
        return 0
    return 2 if unicodedata.east_asian_width(ch) in ("W", "F") else 1


def text_width(s):
    return sum(cell_width(c) for c in s)


SGR = re.compile(r"\x1b\[([0-9;]*)m")
OTHER_CSI = re.compile(r"\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][A-Z0-9]")


def parse(lines):
    """Yield, per line, a list of (col, text, Style) runs."""
    st = Style()
    for raw in lines:
        raw = OTHER_CSI.sub(lambda m: "" if not m.group(0).endswith("m") else m.group(0), raw)
        runs, col, pos = [], 0, 0
        for m in SGR.finditer(raw):
            text = raw[pos:m.start()]
            if text:
                runs.append((col, text, st.copy()))
                col += text_width(text)
            params = [int(x) if x else 0 for x in m.group(1).split(";")] or [0]
            apply_sgr(st, params)
            pos = m.end()
        tail = raw[pos:]
        if tail:
            runs.append((col, tail, st.copy()))
        yield runs


def render(lines, title):
    parsed = list(parse(lines))
    cols = max((sum(text_width(t) for _, t, _ in runs) for runs in parsed), default=80)
    cols = max(cols, 80)
    w = cols * CELL_W + PAD * 2
    h = len(parsed) * CELL_H + PAD * 2 + 34  # room for the window chrome

    out = [
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{w:.0f}" height="{h:.0f}" '
        f'viewBox="0 0 {w:.0f} {h:.0f}" font-family="ui-monospace,SFMono-Regular,'
        f'Menlo,Consolas,&quot;DejaVu Sans Mono&quot;,monospace" font-size="{FONT_SIZE}">',
        f'<title>{escape(title)}</title>',
        f'<rect width="{w:.0f}" height="{h:.0f}" rx="10" fill="{BG}"/>',
        # window chrome: three dots and a title, so it reads as a screenshot
        '<g>'
        '<circle cx="24" cy="20" r="5.5" fill="#ff5f57"/>'
        '<circle cx="42" cy="20" r="5.5" fill="#febc2e"/>'
        '<circle cx="60" cy="20" r="5.5" fill="#28c840"/>'
        f'<text x="{w/2:.0f}" y="25" fill="#6b7280" font-size="12" text-anchor="middle">'
        f'{escape(title)}</text></g>',
    ]
    y0 = PAD + 34
    # background cells first, so text never sits under a later rect
    for row, runs in enumerate(parsed):
        y = y0 + row * CELL_H
        for col, text, st in runs:
            bg = st.bg
            if st.reverse:
                bg = st.fg or FG
            if bg:
                out.append(
                    f'<rect x="{PAD + col * CELL_W:.1f}" y="{y:.1f}" '
                    f'width="{text_width(text) * CELL_W:.1f}" height="{CELL_H:.1f}" fill="{bg}"/>'
                )
    for row, runs in enumerate(parsed):
        y = y0 + row * CELL_H + FONT_SIZE * 0.92
        for col, text, st in runs:
            if not text.strip():
                continue
            fill = st.fg or FG
            if st.reverse:
                fill = st.bg or BG
            attrs = [f'x="{PAD + col * CELL_W:.1f}"', f'y="{y:.1f}"', f'fill="{fill}"',
                     f'textLength="{text_width(text) * CELL_W:.1f}"', 'lengthAdjust="spacingAndGlyphs"',
                     'xml:space="preserve"']
            if st.bold:
                attrs.append('font-weight="600"')
            if st.dim:
                attrs.append('opacity="0.62"')
            if st.italic:
                attrs.append('font-style="italic"')
            if st.underline:
                attrs.append('text-decoration="underline"')
            out.append(f'<text {" ".join(attrs)}>{escape(text)}</text>')
    out.append("</svg>")
    return "\n".join(out)


if __name__ == "__main__":
    src, dst = sys.argv[1], sys.argv[2]
    title = sys.argv[3] if len(sys.argv) > 3 else "agentdeck"
    with open(src, encoding="utf-8", errors="replace") as f:
        lines = [l.rstrip("\n") for l in f]
    while lines and not lines[-1].strip():
        lines.pop()
    with open(dst, "w", encoding="utf-8") as f:
        f.write(render(lines, title))
    print(f"  wrote {dst} ({len(lines)} rows)")
