import { describe, expect, it } from "vitest";
import { applyEvent, initialTable, reducer, sortOrder, tokenPos, type TableState } from "./store";
import type { Character, GameEvent, GameState, Token } from "./types";

const rogue: Token = { id: "tok_1", label: "Rogue", color: "#aa0000", pos: { x: 1, y: 1 }, size: 1, controllers: ["usr_kai"] };

const base: GameState = {
  id: "ses_1",
  seq: 3,
  settings: { diagonal: "5" },
  map: { id: "map_1", image_url: "/uploads/m.png", background: "#e8e0cc", cols: 10, rows: 10, cell_feet: 5, terrain: { "0,0": "wall" } },
  tokens: { tok_1: rogue },
  actors: {},
  members: {},
  encounter: null,
  rolls: [],
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

  it("paints and erases terrain", () => {
    const frozen = structuredClone(base);
    let s = applyEvent(base, ev(4, { name: "CellsPainted", data: { terrain: "water", rect: { from: { x: 2, y: 1 }, to: { x: 1, y: 0 } } } }));
    s = applyEvent(s, ev(5, { name: "CellsPainted", data: { terrain: "clear", cells: [{ x: 0, y: 0 }, { x: 2, y: 1 }] } }));
    s = applyEvent(s, ev(6, { name: "CellsPainted", data: { terrain: "hazard", cells: [{ x: 9, y: 9 }] } }));
    expect(s.map?.terrain).toEqual({ "1,0": "water", "2,0": "water", "1,1": "water", "9,9": "hazard" });
    expect(base).toEqual(frozen);
  });

  it("tracks characters, HP, and conditions", () => {
    const kai: Character = {
      id: "act_kai", kind: "pc", name: "Kai", class: "Rogue", level: 3, ac: 15, speed: 30, init_bonus: 3,
      hp: { current: 24, max: 24, temp: 0 }, conditions: [], controllers: ["usr_kai"], rolls_own_dice: false,
    };
    const frozen = structuredClone(base);
    let s = applyEvent(base, ev(4, { name: "CharacterCreated", data: { character: kai } }));
    s = applyEvent(s, ev(5, { name: "HPChanged", data: { actor: "act_kai", hp: { current: 17, max: 24, temp: 0 }, delta: -7 } }));
    s = applyEvent(s, ev(6, { name: "ConditionAdded", data: { actor: "act_kai", condition: { name: "Prone" } } }));
    s = applyEvent(s, ev(7, { name: "ConditionAdded", data: { actor: "act_kai", condition: { name: "Poisoned", source: "Spider" } } }));
    s = applyEvent(s, ev(8, { name: "ConditionRemoved", data: { actor: "act_kai", name: "Prone" } }));
    s = applyEvent(s, ev(9, { name: "CharacterUpdated", data: { character: { ...s.actors.act_kai!, ac: 16 } } }));
    expect(s.actors.act_kai).toMatchObject({ ac: 16, hp: { current: 17 }, conditions: [{ name: "Poisoned", source: "Spider" }] });
    s = applyEvent(s, ev(10, { name: "CharacterDeleted", data: { actor: "act_kai" } }));
    expect(s.actors).toEqual({});
    expect(base).toEqual(frozen);
  });

  it("changes settings", () => {
    const s = applyEvent(base, ev(4, { name: "SettingsChanged", data: { settings: { diagonal: "5-10-5" } } }));
    expect(s.settings.diagonal).toBe("5-10-5");
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

  it("draws the latest of several pending moves, until the session is gone", () => {
    let s = reducer(withGame(), { type: "move", id: "c1", move: { token: "tok_1", to: { x: 2, y: 2 } } });
    s = reducer(s, { type: "move", id: "c2", move: { token: "tok_1", to: { x: 3, y: 3 } } });
    expect(tokenPos(s, "tok_1")).toEqual({ x: 3, y: 3 });
    s = reducer(s, { type: "gone" });
    expect(tokenPos(s, "tok_1")).toEqual({ x: 1, y: 1 });
  });

  it("catches up from a batch of missed events", () => {
    let s = reducer(withGame(), { type: "event", event: moved(6) }); // missed 4 and 5
    expect(s.stale).toBe(true);
    const placed = (seq: number) => ev(seq, { name: "TokenPlaced", data: { token: { ...rogue, id: `tok_${seq}` } } });
    // The batch overlaps what we have, and runs past the event we saw early.
    s = reducer(s, { type: "events", seq: 6, events: [moved(3), placed(4), placed(5), moved(6)] });
    expect(s.stale).toBe(false);
    expect(s.game?.seq).toBe(6);
    expect(Object.keys(s.game!.tokens).sort()).toEqual(["tok_1", "tok_4", "tok_5"]);
  });

  it("stays stale if a batch has a gap", () => {
    const s = reducer(withGame(), { type: "events", seq: 6, events: [moved(4), moved(6)] });
    expect(s.game?.seq).toBe(4);
    expect(s.stale).toBe(true);
  });

  it("an empty batch means already up to date", () => {
    const s = reducer({ ...withGame(), stale: true }, { type: "events", seq: 3, events: [] });
    expect(s.stale).toBe(false);
  });
});

describe("combat", () => {
  const entry = (actor: string, total: number | null, bonus = 0, tie_break = 0) => ({
    actor, total, roll: null, bonus, tie_break, physical: false,
  });

  it("sorts initiative like the server", () => {
    const got = sortOrder([
      entry("waiting", null, 9),
      entry("low", 5),
      entry("tie-low-bonus", 15, 1, 20),
      entry("tie-high-bonus", 15, 3, 1),
      entry("tie-same-bonus-b", 15, 1, 12),
      entry("high", 22, -1),
    ]).map((e) => e.actor);
    expect(got).toEqual(["high", "tie-high-bonus", "tie-low-bonus", "tie-same-bonus-b", "low", "waiting"]);
  });

  it("runs turns, movement, and actions", () => {
    let s = applyEvent(base, ev(4, { name: "CombatStarted", data: { order: [entry("a", 12, 1, 4), entry("b", null, 2, 9)] } }));
    expect(s.encounter?.order.map((e) => e.actor)).toEqual(["a", "b"]);
    s = applyEvent(s, ev(5, { name: "InitiativeSet", data: { actor: "b", total: 18, roll: 16, bonus: 2, tie_break: 9, physical: true } }));
    expect(s.encounter?.order.map((e) => e.actor)).toEqual(["b", "a"]);
    s = applyEvent(s, ev(6, { name: "TurnStarted", data: { actor: "b", round: 1, movement: 30 } }));
    s = applyEvent(s, ev(7, { name: "MovementSpent", data: { actor: "b", feet: 10, left: 20 } }));
    s = applyEvent(s, ev(8, { name: "ActionUsed", data: { actor: "b", kind: "action", used: true, dash: true, movement_left: 50 } }));
    s = applyEvent(s, ev(9, { name: "ActionUsed", data: { actor: "a", kind: "reaction", used: true, movement_left: 0 } }));
    expect(s.encounter).toMatchObject({
      active: "b",
      economy: { movement_left: 50, action: false, action_dash: true, bonus: true },
      reaction_used: { a: true },
    });
    s = applyEvent(s, ev(10, { name: "TurnStarted", data: { actor: "a", round: 1, movement: 25 } }));
    expect(s.encounter?.reaction_used).toEqual({});
    s = applyEvent(s, ev(11, { name: "CombatEnded", data: {} }));
    expect(s.encounter).toBeNull();
  });
});

describe("log", () => {
  const roll = (seq: number) => ev(seq, { name: "DiceRolled", data: { expr: "1d20", result: { terms: null, total: 12 }, physical: true } });

  it("keeps the latest 50 rolls", () => {
    let s = base;
    for (let i = 4; i < 60; i++) s = applyEvent(s, roll(i));
    expect(s.rolls).toHaveLength(50);
    expect(s.rolls[0]).toMatchObject({ seq: 10, by: "usr_dm", expr: "1d20", physical: true });
  });

  it("adds feed lines for notable events", () => {
    let s = withGame();
    s = reducer(s, { type: "event", event: moved(4) });
    s = reducer(s, { type: "event", event: ev(5, { name: "CombatStarted", data: { order: [] } }) });
    expect(s.feed).toEqual([{ seq: 5, text: "Combat started: roll initiative" }]);
  });
});
