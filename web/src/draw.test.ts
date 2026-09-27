import { describe, expect, it } from "vitest";
import { simplify, split } from "./draw";

describe("pen strokes", () => {
  it("drops points too close together and rounds", () => {
    expect(simplify([0, 0, 0.01, 0.01, 0.02, 0.0, 0.5, 0.5, 0.501, 0.502])).toEqual([0, 0, 0.5, 0.5]);
    expect(simplify([1.2345, 2.3456])).toEqual([1.23, 2.35]);
  });

  it("splits long strokes into pieces that join up", () => {
    const pts = Array.from({ length: 1000 * 2 }, (_, i) => i);
    const pieces = split(pts, 400);
    expect(pieces.every((p) => p.length <= 800)).toBe(true);
    for (let i = 1; i < pieces.length; i++) {
      expect(pieces[i]!.slice(0, 2)).toEqual(pieces[i - 1]!.slice(-2)); // joined
    }
    expect(pieces.at(-1)!.slice(-2)).toEqual(pts.slice(-2));
    expect(split([1, 2, 3, 4])).toEqual([[1, 2, 3, 4]]);
  });
});
