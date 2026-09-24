// Mirrors of the server's JSON. See internal/game and internal/session.

export type UserID = string;
export type TokenID = string;
export type ActorID = string;
export type Role = "dm" | "player" | "spectator";

export interface Cell {
  x: number;
  y: number;
}

export type TerrainKind = "wall" | "difficult" | "water" | "hazard";

export interface MapInfo {
  id: string;
  image_url: string; // empty for a blank map
  background: string;
  cols: number;
  rows: number;
  cell_feet: number;
  terrain: Record<string, TerrainKind>; // keyed by "x,y"
}

export type DiagonalRule = "5" | "5-10-5";

export interface Settings {
  diagonal: DiagonalRule;
}

export interface Rect {
  from: Cell;
  to: Cell;
}

export interface Token {
  id: TokenID;
  actor?: ActorID;
  label: string;
  color: string;
  pos: Cell;
  size: number;
  controllers: UserID[];
}

export type ActorKind = "pc" | "npc" | "monster";

export interface HitPoints {
  current: number;
  max: number;
  temp: number;
}

export interface Condition {
  name: string;
  source?: string;
}

export interface Character {
  id: ActorID;
  kind: ActorKind;
  name: string;
  class: string;
  level: number;
  ac: number;
  speed: number;
  init_bonus: number;
  hp: HitPoints;
  conditions: Condition[];
  controllers: UserID[];
  rolls_own_dice: boolean;
}

export interface Member {
  user_id: UserID;
  display_name: string;
  role: Role;
}

export interface InitEntry {
  actor: ActorID;
  total: number | null; // null while waiting for a physical roll
  roll: number | null;
  bonus: number;
  tie_break: number;
  physical: boolean;
}

export interface TurnEconomy {
  movement_left: number;
  action: boolean; // true = still available
  bonus: boolean;
  action_dash: boolean;
  bonus_dash: boolean;
}

export interface Encounter {
  round: number;
  order: InitEntry[];
  active: ActorID; // "" while waiting for initiative
  economy: TurnEconomy;
  reaction_used: Record<ActorID, boolean>;
}

export type ActionKind = "action" | "bonus" | "reaction";

export interface GameState {
  id: string;
  seq: number;
  settings: Settings;
  map: MapInfo | null;
  tokens: Record<TokenID, Token>;
  actors: Record<ActorID, Character>;
  members: Record<UserID, Member>;
  encounter: Encounter | null;
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
    | { name: "CellsPainted"; data: { terrain: TerrainKind | "clear"; cells?: Cell[]; rect?: Rect } }
    | { name: "SettingsChanged"; data: { settings: Settings } }
    | { name: "TokenPlaced"; data: { token: Token } }
    | { name: "TokenMoved"; data: { token: TokenID; from: Cell; to: Cell } }
    | { name: "TokenRemoved"; data: { token: TokenID } }
    | { name: "CharacterCreated"; data: { character: Character } }
    | { name: "CharacterUpdated"; data: { character: Character } }
    | { name: "CharacterDeleted"; data: { actor: ActorID } }
    | { name: "HPChanged"; data: { actor: ActorID; hp: HitPoints; delta: number } }
    | { name: "ConditionAdded"; data: { actor: ActorID; condition: Condition } }
    | { name: "ConditionRemoved"; data: { actor: ActorID; name: string } }
    | { name: "CombatStarted"; data: { order: InitEntry[] } }
    | {
        name: "InitiativeSet";
        data: { actor: ActorID; total: number; roll?: number; bonus: number; tie_break: number; physical: boolean };
      }
    | { name: "CombatantRemoved"; data: { actor: ActorID } }
    | { name: "TurnStarted"; data: { actor: ActorID; round: number; movement: number } }
    | { name: "TurnEnded"; data: { actor: ActorID } }
    | { name: "MovementSpent"; data: { actor: ActorID; feet: number; left: number } }
    | {
        name: "ActionUsed";
        data: { actor: ActorID; kind: ActionKind; used: boolean; dash?: boolean; movement_left: number };
      }
    | { name: "CombatEnded"; data: Record<string, never> }
  );

export type ServerMsg =
  | { type: "welcome"; session: string; user: UserID; role: Role; caps: string[] }
  | { type: "snapshot"; seq: number; state: GameState }
  | ({ type: "event" } & GameEvent)
  | { type: "ack"; id: string; seq: number }
  | { type: "reject"; id: string; code: string; message: string }
  | { type: "error"; message: string }
  | { type: "pong" };
