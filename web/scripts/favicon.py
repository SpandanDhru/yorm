"""Draws the favicon: a 16x16 pixel golden sword, in the palette of the
home page's 3D sword. Run from web/: python3 scripts/favicon.py public
"""
import zlib, struct, sys
# 16x16 pixel art. Blade points up-right, grip down-left.
# o outline (darkest), 1 lightest .. 4 darkest gold, . transparent
ART = [
    "............ooo.",
    "...........o11o.",
    "..........o112o.",
    ".........o112o..",
    "........o112o...",
    ".......o112o....",
    "..oo..o112o.....",
    "..o3oo112o......",
    "...o3o12o.......",
    "....o33o........",
    "...o4o33o.......",
    "..o4o.oo3o......",
    ".o4o....oo......",
    "o44o............",
    "o3o.............",
    ".o..............",
]
PAL = {"o": (0x5a, 0x3a, 0x04), "1": (0xff, 0xf1, 0xa8), "2": (0xff, 0xd2, 0x3f), "3": (0xe8, 0xa9, 0x0e), "4": (0xb0, 0x76, 0x06)}
assert all(len(r) == 16 for r in ART) and len(ART) == 16

def png(scale):
    n = 16 * scale
    rows = []
    for y in range(n):
        r = bytearray([0])
        for x in range(n):
            c = ART[y // scale][x // scale]
            r += bytes((*PAL[c], 255)) if c in PAL else bytes((0, 0, 0, 0))
        rows.append(bytes(r))
    ch = lambda t, d: struct.pack(">I", len(d)) + t + d + struct.pack(">I", zlib.crc32(t + d) & 0xffffffff)
    return b"\x89PNG\r\n\x1a\n" + ch(b"IHDR", struct.pack(">IIBBBBB", n, n, 8, 6, 0, 0, 0)) + ch(b"IDAT", zlib.compress(b"".join(rows), 9)) + ch(b"IEND", b"")

def svg():
    rects = []
    for y, row in enumerate(ART):
        for x, c in enumerate(row):
            if c in PAL:
                rects.append(f'<rect x="{x}" y="{y}" width="1" height="1" fill="#{"%02x%02x%02x" % PAL[c]}"/>')
    return '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" shape-rendering="crispEdges">' + "".join(rects) + "</svg>\n"

if len(sys.argv) > 1:
    out = sys.argv[1]
    open(f"{out}/favicon.svg", "w").write(svg())
    open(f"{out}/favicon-32.png", "wb").write(png(2))
    open(f"{out}/apple-touch-icon.png", "wb").write(png(12))  # 192px; iOS scales it
