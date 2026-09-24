// Mirrors of the server's JSON. See internal/game and internal/session.

export type UserID = string;
export type TokenID = string;
export type Role = "dm" | "player" | "spectator";

export interface Cell {
  x: number;
  y: number;
}

export interface MapInfo {
  id: string;
  image_url: string;
  cols: number;
  rows: number;
  cell_feet: number;
}

export interface Token {
  id: TokenID;
  label: string;
  color: string;
  pos: Cell;
  size: number;
  controllers: UserID[];
}

export interface Member {
  user_id: UserID;
  display_name: string;
  role: Role;
}

export interface GameState {
  id: string;
  seq: number;
  map: MapInfo | null;
  tokens: Record<TokenID, Token>;
  members: Record<UserID, Member>;
}

interface EventBase {
  seq: number;
  by: UserID;
  cause?: string;
  at: string;
}

export type GameEvent = EventBase &
  (
    | { name: "MemberJoined"; data: { member: Member } }
    | { name: "MapSet"; data: { map: MapInfo } }
    | { name: "TokenPlaced"; data: { token: Token } }
    | { name: "TokenMoved"; data: { token: TokenID; from: Cell; to: Cell } }
    | { name: "TokenRemoved"; data: { token: TokenID } }
  );

export type ServerMsg =
  | { type: "welcome"; session: string; user: UserID; role: Role; caps: string[] }
  | { type: "snapshot"; seq: number; state: GameState }
  | ({ type: "event" } & GameEvent)
  | { type: "ack"; id: string; seq: number }
  | { type: "reject"; id: string; code: string; message: string }
  | { type: "error"; message: string }
  | { type: "pong" };
