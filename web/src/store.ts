// Client state: the server's state as last seen, plus this client's moves
// that are still waiting for the server's answer. Holds no game rules; the
// server validates everything.
import type { ActorID, Cell, Character, Encounter, GameEvent, GameState, InitEntry, Rect, Token, TokenID, UserID } from "./types";

export const cellKey = (c: Cell) => `${c.x},${c.y}`;

// rectCells lists every cell of an inclusive rectangle, corners in any order.
export function rectCells(r: Rect): Cell[] {
  const cells: Cell[] = [];
  for (let y = Math.min(r.from.y, r.to.y); y <= Math.max(r.from.y, r.to.y); y++) {
    for (let x = Math.min(r.from.x, r.to.x); x <= Math.max(r.from.x, r.to.x); x++) cells.push({ x, y });
  }
  return cells;
}

export interface PendingMove {
  token: TokenID;
  to: Cell;
}

export interface TableState {
  game: GameState | null;
  // Optimistic moves by command ID. A token is drawn at its pending
  // position until the server's event or reject settles the command.
  pending: Record<string, PendingMove>;
  // Set when an event arrives out of order; the connection then resyncs.
  stale: boolean;
}

export const initialTable: TableState = { game: null, pending: {}, stale: false };

export type Action =
  | { type: "snapshot"; state: GameState }
  | { type: "event"; event: GameEvent }
  | { type: "move"; id: string; move: PendingMove }
  | { type: "settle"; id: string }
  | { type: "disconnected" };

// applyEvent mirrors game.State.Apply on the server.
export function applyEvent(s: GameState, ev: GameEvent): GameState {
  const next = { ...s, seq: ev.seq };
  switch (ev.name) {
    case "MemberJoined":
      next.members = { ...s.members, [ev.data.member.user_id]: ev.data.member };
      break;
    case "MapSet":
      next.map = { ...ev.data.map, terrain: ev.data.map.terrain ?? {} };
      break;
    case "CellsPainted": {
      if (!s.map) break;
      const terrain = { ...s.map.terrain };
      const { terrain: kind, cells = [], rect } = ev.data;
      for (const c of rect ? [...cells, ...rectCells(rect)] : cells) {
        if (kind === "clear") delete terrain[cellKey(c)];
        else terrain[cellKey(c)] = kind;
      }
      next.map = { ...s.map, terrain };
      break;
    }
    case "SettingsChanged":
      next.settings = ev.data.settings;
      break;
    case "TokenPlaced":
      next.tokens = { ...s.tokens, [ev.data.token.id]: ev.data.token };
      break;
    case "TokenMoved": {
      const t = s.tokens[ev.data.token];
      if (t) next.tokens = { ...s.tokens, [t.id]: { ...t, pos: ev.data.to } };
      break;
    }
    case "TokenRemoved": {
      const { [ev.data.token]: _, ...rest } = s.tokens;
      next.tokens = rest;
      break;
    }
    case "CharacterCreated":
    case "CharacterUpdated":
      next.actors = { ...s.actors, [ev.data.character.id]: ev.data.character };
      break;
    case "CharacterDeleted": {
      const { [ev.data.actor]: _, ...rest } = s.actors;
      next.actors = rest;
      next.encounter = removeCombatant(s.encounter, ev.data.actor);
      break;
    }
    case "CombatStarted":
      next.encounter = { round: 1, order: sortOrder(ev.data.order), active: "", economy: NO_ECONOMY, reaction_used: {} };
      break;
    case "InitiativeSet": {
      const e = s.encounter;
      if (!e) break;
      const d = ev.data;
      const old = e.order.find((x) => x.actor === d.actor);
      const entry: InitEntry = {
        actor: d.actor, total: d.total, roll: d.roll ?? null, bonus: d.bonus,
        tie_break: old ? old.tie_break : d.tie_break, physical: d.physical,
      };
      const order = old ? e.order.map((x) => (x.actor === d.actor ? entry : x)) : [...e.order, entry];
      next.encounter = { ...e, order: sortOrder(order) };
      break;
    }
    case "CombatantRemoved":
      next.encounter = removeCombatant(s.encounter, ev.data.actor);
      break;
    case "TurnStarted": {
      const e = s.encounter;
      if (!e) break;
      const { [ev.data.actor]: _, ...reactions } = e.reaction_used;
      next.encounter = {
        ...e,
        active: ev.data.actor,
        round: ev.data.round,
        economy: { ...NO_ECONOMY, movement_left: ev.data.movement, action: true, bonus: true },
        reaction_used: reactions,
      };
      break;
    }
    case "MovementSpent":
      if (s.encounter?.active === ev.data.actor) {
        next.encounter = { ...s.encounter, economy: { ...s.encounter.economy, movement_left: ev.data.left } };
      }
      break;
    case "ActionUsed": {
      const e = s.encounter;
      const d = ev.data;
      if (!e) break;
      if (d.kind === "reaction") {
        const { [d.actor]: _, ...rest } = e.reaction_used;
        next.encounter = { ...e, reaction_used: d.used ? { ...rest, [d.actor]: true } : rest };
      } else if (e.active === d.actor) {
        const economy = { ...e.economy, movement_left: d.movement_left };
        if (d.kind === "action") Object.assign(economy, { action: !d.used, action_dash: d.used && !!d.dash });
        else Object.assign(economy, { bonus: !d.used, bonus_dash: d.used && !!d.dash });
        next.encounter = { ...e, economy };
      }
      break;
    }
    case "CombatEnded":
      next.encounter = null;
      break;
    case "HPChanged":
      next.actors = updateActor(s, ev.data.actor, (a) => ({ ...a, hp: ev.data.hp }));
      break;
    case "ConditionAdded":
      next.actors = updateActor(s, ev.data.actor, (a) => ({ ...a, conditions: [...a.conditions, ev.data.condition] }));
      break;
    case "ConditionRemoved":
      next.actors = updateActor(s, ev.data.actor, (a) => ({
        ...a,
        conditions: a.conditions.filter((c) => c.name !== ev.data.name),
      }));
      break;
  }
  return next;
}

const NO_ECONOMY = { movement_left: 0, action: false, bonus: false, action_dash: false, bonus_dash: false };

// sortOrder mirrors Encounter.sortOrder on the server: total (waiting last),
// then bonus, then tie-break roll, then ID.
export function sortOrder(order: InitEntry[]): InitEntry[] {
  // Byte-wise like Go's cmp.Compare, unlike localeCompare.
  const cmp = <T extends number | string>(a: T, b: T) => (a < b ? -1 : a > b ? 1 : 0);
  return [...order].sort((a, b) => {
    if ((a.total === null) !== (b.total === null)) return a.total === null ? 1 : -1;
    return (
      (a.total !== null && b.total !== null ? cmp(b.total, a.total) : 0) ||
      cmp(b.bonus, a.bonus) ||
      cmp(b.tie_break, a.tie_break) ||
      cmp(a.actor, b.actor)
    );
  });
}

function removeCombatant(e: Encounter | null, actor: ActorID): Encounter | null {
  return e && { ...e, order: e.order.filter((x) => x.actor !== actor) };
}

function updateActor(s: GameState, id: ActorID, f: (a: Character) => Character): Record<ActorID, Character> {
  const a = s.actors[id];
  return a ? { ...s.actors, [id]: f(a) } : s.actors;
}

// canControl mirrors game.State.CanControl: may user move token t?
export function canControl(g: GameState, user: UserID, t: Token): boolean {
  if (g.members[user]?.role === "dm" || t.controllers.includes(user)) return true;
  return !!t.actor && !!g.actors[t.actor]?.controllers.includes(user);
}

// canEdit mirrors game.State.CanEdit: may user change character a?
export function canEdit(g: GameState, user: UserID, a: Character): boolean {
  return g.members[user]?.role === "dm" || a.controllers.includes(user);
}

export function tokenFor(g: GameState, actor: ActorID): Token | undefined {
  return Object.values(g.tokens).find((t) => t.actor === actor);
}

// freeCell finds the first spot, row by row, where a token of the given
// size overlaps no other token or wall.
export function freeCell(game: GameState, size: number): Cell | null {
  const map = game.map;
  if (!map) return null;
  const taken = new Set<string>(Object.keys(map.terrain).filter((k) => map.terrain[k] === "wall"));
  for (const t of Object.values(game.tokens)) {
    for (let dx = 0; dx < t.size; dx++) for (let dy = 0; dy < t.size; dy++) taken.add(`${t.pos.x + dx},${t.pos.y + dy}`);
  }
  for (let y = 0; y + size <= map.rows; y++) {
    for (let x = 0; x + size <= map.cols; x++) {
      let free = true;
      for (let dx = 0; dx < size && free; dx++) for (let dy = 0; dy < size && free; dy++) free = !taken.has(`${x + dx},${y + dy}`);
      if (free) return { x, y };
    }
  }
  return { x: 0, y: 0 }; // full map: stack on the corner, the DM can drag it
}

function without<T>(rec: Record<string, T>, key: string | undefined): Record<string, T> {
  if (key === undefined || !(key in rec)) return rec;
  const { [key]: _, ...rest } = rec;
  return rest;
}

export function reducer(s: TableState, a: Action): TableState {
  switch (a.type) {
    case "snapshot":
      return { ...s, game: a.state, stale: false };
    case "event": {
      const g = s.game;
      if (!g || s.stale) return s; // waiting for a snapshot
      if (a.event.seq <= g.seq) return s; // already applied
      if (a.event.seq > g.seq + 1) return { ...s, stale: true }; // missed some
      return { ...s, game: applyEvent(g, a.event), pending: without(s.pending, a.event.cause) };
    }
    case "move":
      return { ...s, pending: { ...s.pending, [a.id]: a.move } };
    case "settle":
      return { ...s, pending: without(s.pending, a.id) };
    case "disconnected":
      // Unanswered moves may or may not have landed; the snapshot after
      // reconnecting says which.
      return { ...s, pending: {} };
  }
}

// tokenPos is where to draw a token: its latest pending move, if any.
export function tokenPos(s: TableState, id: TokenID): Cell | undefined {
  let pos = s.game?.tokens[id]?.pos;
  for (const m of Object.values(s.pending)) {
    if (m.token === id) pos = m.to;
  }
  return pos;
}
