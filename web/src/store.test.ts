import { describe, expect, it } from "vitest";
import { applyEvent, initialTable, reducer, tokenPos, type TableState } from "./store";
import type { GameEvent, GameState, Token } from "./types";

const rogue: Token = { id: "tok_1", label: "Rogue", color: "#aa0000", pos: { x: 1, y: 1 }, size: 1, controllers: ["usr_kai"] };

const base: GameState = {
  id: "ses_1",
  seq: 3,
  map: { id: "map_1", image_url: "/uploads/m.png", cols: 10, rows: 10, cell_feet: 5 },
  tokens: { tok_1: rogue },
  members: {},
};

function ev(seq: number, e: Omit<GameEvent, "seq" | "by" | "at">): GameEvent {
  return { seq, by: "usr_dm", at: "2026-10-02T00:00:00Z", ...e } as GameEvent;
}

const moved = (seq: number, cause?: string) =>
  ev(seq, { name: "TokenMoved", data: { token: "tok_1", from: { x: 1, y: 1 }, to: { x: 5, y: 6 } }, cause });

function withGame(): TableState {
  return reducer(initialTable, { type: "snapshot", state: base });
}

describe("applyEvent", () => {
  it("applies each event kind without mutating the input", () => {
    const frozen = structuredClone(base);
    let s = applyEvent(base, ev(4, { name: "MemberJoined", data: { member: { user_id: "usr_kai", display_name: "Kai", role: "player" } } }));
    s = applyEvent(s, ev(5, { name: "TokenPlaced", data: { token: { ...rogue, id: "tok_2" } } }));
    s = applyEvent(s, moved(6));
    s = applyEvent(s, ev(7, { name: "TokenRemoved", data: { token: "tok_2" } }));
    s = applyEvent(s, ev(8, { name: "MapSet", data: { map: { ...base.map!, cols: 20 } } }));

    expect(s.seq).toBe(8);
    expect(s.members.usr_kai?.display_name).toBe("Kai");
    expect(Object.keys(s.tokens)).toEqual(["tok_1"]);
    expect(s.tokens.tok_1?.pos).toEqual({ x: 5, y: 6 });
    expect(s.map?.cols).toBe(20);
    expect(base).toEqual(frozen);
  });
});

describe("reducer", () => {
  it("applies the next event in order", () => {
    const s = reducer(withGame(), { type: "event", event: moved(4) });
    expect(s.game?.seq).toBe(4);
    expect(s.game?.tokens.tok_1?.pos).toEqual({ x: 5, y: 6 });
  });

  it("ignores events it already has", () => {
    const s = withGame();
    expect(reducer(s, { type: "event", event: moved(3) })).toBe(s);
  });

  it("marks itself stale on a gap and ignores events until a snapshot", () => {
    let s = reducer(withGame(), { type: "event", event: moved(6) });
    expect(s.stale).toBe(true);
    expect(s.game?.seq).toBe(3);
    s = reducer(s, { type: "event", event: moved(4) });
    expect(s.game?.seq).toBe(3);
    s = reducer(s, { type: "snapshot", state: { ...base, seq: 6 } });
    expect(s.stale).toBe(false);
    expect(s.game?.seq).toBe(6);
  });

  it("shows an optimistic move until its event arrives", () => {
    let s = reducer(withGame(), { type: "move", id: "c1", move: { token: "tok_1", to: { x: 5, y: 6 } } });
    expect(tokenPos(s, "tok_1")).toEqual({ x: 5, y: 6 });
    expect(s.game?.tokens.tok_1?.pos).toEqual({ x: 1, y: 1 });
    s = reducer(s, { type: "event", event: moved(4, "c1") });
    expect(s.pending).toEqual({});
    expect(tokenPos(s, "tok_1")).toEqual({ x: 5, y: 6 });
  });

  it("reverts an optimistic move when it is rejected", () => {
    let s = reducer(withGame(), { type: "move", id: "c1", move: { token: "tok_1", to: { x: 9, y: 9 } } });
    s = reducer(s, { type: "settle", id: "c1" });
    expect(tokenPos(s, "tok_1")).toEqual({ x: 1, y: 1 });
  });

  it("draws the latest of several pending moves", () => {
    let s = reducer(withGame(), { type: "move", id: "c1", move: { token: "tok_1", to: { x: 2, y: 2 } } });
    s = reducer(s, { type: "move", id: "c2", move: { token: "tok_1", to: { x: 3, y: 3 } } });
    expect(tokenPos(s, "tok_1")).toEqual({ x: 3, y: 3 });
    s = reducer(s, { type: "disconnected" });
    expect(tokenPos(s, "tok_1")).toEqual({ x: 1, y: 1 });
  });
});
