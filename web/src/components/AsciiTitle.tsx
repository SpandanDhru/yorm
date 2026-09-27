// AsciiTitle draws figlet-style ASCII art as crisp SVG: each "█" is a solid
// cell and each box-drawing character (the letters' shadow) a thin line in
// the directions it connects. Browsers draw those characters with whatever
// font they have, often badly; this looks the same everywhere.

// Which way each box-drawing character's lines run.
const LINKS: Record<string, { up?: boolean; down?: boolean; left?: boolean; right?: boolean }> = {
  "═": { left: true, right: true },
  "║": { up: true, down: true },
  "╔": { right: true, down: true },
  "╗": { left: true, down: true },
  "╚": { right: true, up: true },
  "╝": { left: true, up: true },
};

const W = 1; // a cell is twice as tall as it is wide, like a terminal
const H = 2;

export function AsciiTitle({ art, label }: { art: string; label: string }) {
  const rows = art.split("\n");
  const cols = Math.max(...rows.map((r) => [...r].length));
  const blocks: string[] = [];
  const lines: string[] = [];
  rows.forEach((row, y) => {
    [...row].forEach((ch, x) => {
      const cx = x * W + W / 2;
      const cy = y * H + H / 2;
      if (ch === "█") {
        blocks.push(`M${x * W} ${y * H}h${W}v${H}h${-W}z`);
        return;
      }
      const l = LINKS[ch];
      if (!l) return;
      if (l.left) lines.push(`M${cx} ${cy}H${x * W}`);
      if (l.right) lines.push(`M${cx} ${cy}H${(x + 1) * W}`);
      if (l.up) lines.push(`M${cx} ${cy}V${y * H}`);
      if (l.down) lines.push(`M${cx} ${cy}V${(y + 1) * H}`);
    });
  });
  return (
    <svg
      className="ascii-art"
      viewBox={`0 0 ${cols * W} ${rows.length * H}`}
      role="img"
      aria-label={label}
      shapeRendering="crispEdges"
    >
      <title>{label}</title>
      <path d={lines.join("")} fill="none" stroke="currentColor" strokeOpacity={0.5} strokeWidth={0.22} strokeLinecap="square" />
      <path d={blocks.join("")} fill="currentColor" />
    </svg>
  );
}
