import { useState, type FormEvent } from "react";
import { inviteLink, uploadMap, type Seat } from "../api";
import type { Cell, GameState, TokenID } from "../types";

interface Props {
  seat: Seat;
  game: GameState;
  selected: TokenID | null;
  command(name: string, args: unknown): void;
}

export function Sidebar({ seat, game, selected, command }: Props) {
  const isDM = seat.role === "dm";
  const token = selected ? game.tokens[selected] : undefined;
  const members = Object.values(game.members).sort((a, b) => a.display_name.localeCompare(b.display_name));

  return (
    <aside className="sidebar">
      {isDM && <Invite seat={seat} />}

      <section>
        <h3>At the table</h3>
        <ul className="list">
          {members.map((m) => (
            <li key={m.user_id}>
              {m.display_name}
              {m.role === "dm" && <span className="tag">DM</span>}
              {m.user_id === seat.user && <span className="muted"> (you)</span>}
            </li>
          ))}
        </ul>
      </section>

      {token && (
        <section>
          <h3>{token.label}</h3>
          <p className="muted">
            Cell {token.pos.x + 1}, {token.pos.y + 1} · controlled by{" "}
            {token.controllers.map((u) => game.members[u]?.display_name ?? u).join(", ") || "the DM"}
          </p>
          {isDM && (
            <button className="danger" onClick={() => command("remove_token", { token: token.id })}>
              Remove token
            </button>
          )}
        </section>
      )}

      {isDM && game.map && <AddToken game={game} command={command} />}
      {isDM && <MapUpload seat={seat} hasMap={!!game.map} />}
    </aside>
  );
}

function Invite({ seat }: { seat: Seat }) {
  const link = inviteLink(seat);
  const [copied, setCopied] = useState(false);
  if (!link) return null;
  return (
    <section>
      <h3>Invite players</h3>
      <div className="row">
        <input readOnly value={link} onFocus={(e) => e.target.select()} />
        <button
          onClick={() => {
            void navigator.clipboard?.writeText(link).then(() => {
              setCopied(true);
              setTimeout(() => setCopied(false), 1500);
            });
          }}
        >
          {copied ? "Copied" : "Copy"}
        </button>
      </div>
    </section>
  );
}

const SIZES = [
  { size: 1, name: "Medium" },
  { size: 2, name: "Large" },
  { size: 3, name: "Huge" },
  { size: 4, name: "Gargantuan" },
];

function AddToken({ game, command }: { game: GameState; command: Props["command"] }) {
  const [label, setLabel] = useState("");
  const [color, setColor] = useState("#c0392b");
  const [size, setSize] = useState(1);
  const [controller, setController] = useState("");
  const players = Object.values(game.members).filter((m) => m.role === "player");

  function submit(e: FormEvent) {
    e.preventDefault();
    const at = freeCell(game, size);
    if (!at) return;
    command("place_token", { label, color, size, at, controllers: controller ? [controller] : [] });
    setLabel("");
  }

  return (
    <section>
      <h3>Add token</h3>
      <form className="stack" onSubmit={submit}>
        <div className="row">
          <input value={label} onChange={(e) => setLabel(e.target.value)} placeholder="Label" maxLength={32} required />
          <input type="color" value={color} onChange={(e) => setColor(e.target.value)} aria-label="Color" />
        </div>
        <div className="row">
          <select value={size} onChange={(e) => setSize(Number(e.target.value))} aria-label="Size">
            {SIZES.map((s) => (
              <option key={s.size} value={s.size}>
                {s.name}
              </option>
            ))}
          </select>
          <select value={controller} onChange={(e) => setController(e.target.value)} aria-label="Controlled by">
            <option value="">DM only</option>
            {players.map((m) => (
              <option key={m.user_id} value={m.user_id}>
                {m.display_name}
              </option>
            ))}
          </select>
        </div>
        <button>Place</button>
      </form>
    </section>
  );
}

// freeCell finds the first spot, row by row, where a token of the given
// size overlaps no other token.
export function freeCell(game: GameState, size: number): Cell | null {
  const map = game.map;
  if (!map) return null;
  const taken = new Set<string>();
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

function MapUpload({ seat, hasMap }: { seat: Seat; hasMap: boolean }) {
  const [file, setFile] = useState<File | null>(null);
  const [cols, setCols] = useState(20);
  const [rows, setRows] = useState(15);
  const [cellFeet, setCellFeet] = useState(5);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function pick(f: File | null) {
    setFile(f);
    if (!f) return;
    // Suggest rows from the image's shape so cells stay square.
    try {
      const bmp = await createImageBitmap(f);
      setRows(Math.max(1, Math.round((cols * bmp.height) / bmp.width)));
      bmp.close();
    } catch {
      // Not decodable here; the server checks the type anyway.
    }
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!file) return;
    setBusy(true);
    setError("");
    try {
      await uploadMap(seat, file, { cols, rows, cellFeet });
      setFile(null);
      (e.target as HTMLFormElement).reset();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <section>
      <h3>{hasMap ? "Change map" : "Upload map"}</h3>
      <form className="stack" onSubmit={submit}>
        <input type="file" accept="image/png,image/jpeg,image/webp,image/gif" onChange={(e) => void pick(e.target.files?.[0] ?? null)} required />
        <div className="row">
          <label>
            Columns
            <input type="number" min={1} max={200} value={cols} onChange={(e) => setCols(Number(e.target.value))} />
          </label>
          <label>
            Rows
            <input type="number" min={1} max={200} value={rows} onChange={(e) => setRows(Number(e.target.value))} />
          </label>
          <label>
            Feet/cell
            <input type="number" min={1} max={100} value={cellFeet} onChange={(e) => setCellFeet(Number(e.target.value))} />
          </label>
        </div>
        {error && <p className="error">{error}</p>}
        <button disabled={busy || !file}>{busy ? "Uploading…" : "Upload"}</button>
      </form>
    </section>
  );
}
