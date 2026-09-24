import { describe, expect, it } from "vitest";
import { pathCost } from "./move";
import cases from "./testdata/pathcost.json";
import type { DiagonalRule, TerrainKind } from "./types";

// Same encoding as mapOf in internal/game/game_test.go.
const KINDS: Record<string, TerrainKind> = { "#": "wall", "~": "water", ":": "difficult", "!": "hazard" };

function mapOf(rows: string[]) {
  const terrain: Record<string, TerrainKind> = {};
  rows.forEach((row, y) =>
    [...row].forEach((ch, x) => {
      const k = KINDS[ch];
      if (k) terrain[`${x},${y}`] = k;
    }),
  );
  return { cols: rows[0]!.length, rows: rows.length, cell_feet: 5, terrain };
}

describe("pathCost matches the server", () => {
  it.each(cases)("$name", (c) => {
    const got = pathCost(mapOf(c.rows), c.rule as DiagonalRule, c.size, c.from, c.to, c.limit);
    expect(got).toBe(c.ok ? c.feet : null);
  });
});
