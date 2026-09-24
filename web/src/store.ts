// Client state: the server's state as last seen, plus this client's moves
// that are still waiting for the server's answer. Holds no game rules; the
// server validates everything.
import type { Cell, GameEvent, GameState, TokenID } from "./types";

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
      next.map = ev.data.map;
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
  }
  return next;
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
