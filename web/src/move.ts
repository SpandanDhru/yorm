// A copy of game.PathCost (internal/game/move.go) for previewing how far a
// drag goes. The server still decides; move.test.ts runs this against the
// same cases as the Go tests (testdata/pathcost.json) so the two can't drift.
import type { Cell, DiagonalRule, MapInfo } from "./types";

const STEPS: Cell[] = [
  { x: 1, y: 0 }, { x: -1, y: 0 }, { x: 0, y: 1 }, { x: 0, y: -1 },
  { x: 1, y: 1 }, { x: 1, y: -1 }, { x: -1, y: 1 }, { x: -1, y: -1 },
];

type Grid = Pick<MapInfo, "cols" | "rows" | "cell_feet" | "terrain">;

// pathCost returns the cheapest cost in feet, or null if unreachable within
// limit (negative: no limit). See game.PathCost for the rules.
export function pathCost(m: Grid, rule: DiagonalRule, size: number, from: Cell, to: Cell, limit = -1): number | null {
  if (from.x === to.x && from.y === to.y) return 0;
  const anyOf = (c: Cell, kinds: string[]) => {
    for (let dy = 0; dy < size; dy++)
      for (let dx = 0; dx < size; dx++) {
        const k = m.terrain[`${c.x + dx},${c.y + dy}`];
        if (k && kinds.includes(k)) return true;
      }
    return false;
  };
  const blocked = (c: Cell) => c.x < 0 || c.y < 0 || c.x + size > m.cols || c.y + size > m.rows || anyOf(c, ["wall"]);
  const difficult = (c: Cell) => anyOf(c, ["difficult", "water"]);
  if (blocked(to)) return null;

  // Dijkstra over (cell, parity of diagonal steps), as on the server.
  const key = (c: Cell, parity: boolean) => (c.y * m.cols + c.x) * 2 + (parity ? 1 : 0);
  const dist = new Map<number, number>([[key(from, false), 0]]);
  const heap = new MinHeap<{ c: Cell; parity: boolean; cost: number }>((a, b) => a.cost - b.cost);
  heap.push({ c: from, parity: false, cost: 0 });
  while (heap.size > 0) {
    const cur = heap.pop()!;
    if (cur.cost > (dist.get(key(cur.c, cur.parity)) ?? Infinity)) continue;
    if (cur.c.x === to.x && cur.c.y === to.y) return cur.cost;
    for (const d of STEPS) {
      const next = { x: cur.c.x + d.x, y: cur.c.y + d.y };
      if (blocked(next)) continue;
      const diagonal = d.x !== 0 && d.y !== 0;
      if (diagonal && (blocked({ x: next.x, y: cur.c.y }) || blocked({ x: cur.c.x, y: next.y }))) continue;
      let step = m.cell_feet;
      let parity = cur.parity;
      if (diagonal && rule === "5-10-5") {
        if (parity) step *= 2;
        parity = !parity;
      }
      if (difficult(next)) step *= 2;
      const cost = cur.cost + step;
      if (limit >= 0 && cost > limit) continue;
      const k = key(next, parity);
      if (cost < (dist.get(k) ?? Infinity)) {
        dist.set(k, cost);
        heap.push({ c: next, parity, cost });
      }
    }
  }
  return null;
}

class MinHeap<T> {
  private items: T[] = [];
  constructor(private readonly less: (a: T, b: T) => number) {}
  get size() {
    return this.items.length;
  }
  push(x: T) {
    const a = this.items;
    a.push(x);
    for (let i = a.length - 1; i > 0; ) {
      const p = (i - 1) >> 1;
      if (this.less(a[i]!, a[p]!) >= 0) break;
      [a[i], a[p]] = [a[p]!, a[i]!];
      i = p;
    }
  }
  pop(): T | undefined {
    const a = this.items;
    const top = a[0];
    const last = a.pop();
    if (a.length > 0 && last !== undefined) {
      a[0] = last;
      for (let i = 0; ; ) {
        const l = 2 * i + 1;
        const r = l + 1;
        let m = i;
        if (l < a.length && this.less(a[l]!, a[m]!) < 0) m = l;
        if (r < a.length && this.less(a[r]!, a[m]!) < 0) m = r;
        if (m === i) break;
        [a[i], a[m]] = [a[m]!, a[i]!];
        i = m;
      }
    }
    return top;
  }
}
