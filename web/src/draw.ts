// Pen strokes: thin out and split the points a pointer produces, before
// sending them as draw commands (at most 400 points each; see
// game.maxDrawPoints).

const MAX_POINTS = 400;

// simplify drops points closer than minGap (in cells) to the last one kept,
// rounds to 1/100 of a cell, and always keeps the last point.
export function simplify(points: number[], minGap = 0.04): number[] {
  const out: number[] = [];
  const round = (v: number) => Math.round(v * 100) / 100;
  for (let i = 0; i + 1 < points.length; i += 2) {
    const x = round(points[i]!);
    const y = round(points[i + 1]!);
    const n = out.length;
    const last = i + 2 >= points.length;
    if (n === 0 || last || Math.hypot(x - out[n - 2]!, y - out[n - 1]!) >= minGap) {
      if (n > 0 && x === out[n - 2] && y === out[n - 1]) continue;
      out.push(x, y);
    }
  }
  return out;
}

// split breaks a long stroke into pieces of at most MAX_POINTS points that
// share their joining point, so the pieces draw as one line.
export function split(points: number[], max = MAX_POINTS): number[][] {
  const pieces: number[][] = [];
  for (let start = 0; start < points.length - 2 || pieces.length === 0; start += (max - 1) * 2) {
    pieces.push(points.slice(start, start + max * 2));
    if (start + max * 2 >= points.length) break;
  }
  return pieces;
}
