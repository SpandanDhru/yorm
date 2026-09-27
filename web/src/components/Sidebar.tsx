import { useState, type FormEvent } from "react";
import { inviteLink, uploadMap, type Seat } from "../api";
import { freeCell, tokenFor } from "../store";
import type { GameState, MapInfo, Token, TokenID } from "../types";
import { CharactersPanel } from "./Characters";
import { NumberInput } from "./NumberInput";
import { Initiative } from "./Initiative";
import { LogPanel } from "./Log";
import type { FeedLine } from "../store";

interface Props {
  seat: Seat;
  game: GameState;
  feed: FeedLine[];
  selected: TokenID | null;
  command(name: string, args: unknown): void;
}

type Tab = "characters" | "log" | "map";

export function Sidebar({ seat, game, feed, selected, command }: Props) {
  const isDM = seat.role === "dm";
  const [tab, setTab] = useState<Tab>("characters");
  const token = selected ? game.tokens[selected] : undefined;
  const members = Object.values(game.members).sort((a, b) => a.display_name.localeCompare(b.display_name));

  return (
    <aside className="sidebar">
      <Initiative seat={seat} game={game} command={command} />
      {isDM && !game.encounter && <Invite seat={seat} />}

      {token && !token.actor && (
        <section>
          <h3>{token.label}</h3>
          <p className="muted">
            Cell {token.pos.x + 1}, {token.pos.y + 1} · moved by{" "}
            {token.controllers.map((u) => game.members[u]?.display_name ?? u).join(", ") || "the DM"}
          </p>
          {isDM && (
            <div className="row">
              <HideButton token={token} command={command} />
              <button className="danger" onClick={() => command("remove_token", { token: token.id })}>
                Remove token
              </button>
            </div>
          )}
        </section>
      )}

      <div className="tabs" role="tablist">
        <button role="tab" aria-selected={tab === "characters"} onClick={() => setTab("characters")}>
          Characters
        </button>
        <button role="tab" aria-selected={tab === "log"} onClick={() => setTab("log")}>
          Log
        </button>
        {isDM && (
          <button role="tab" aria-selected={tab === "map"} onClick={() => setTab("map")}>
            Map
          </button>
        )}
      </div>

      {tab === "log" && <LogPanel game={game} feed={feed} command={command} isDM={isDM} />}

      {tab === "characters" && (
        <>
          <CharactersPanel seat={seat} game={game} command={command} focus={token?.actor ?? null} />
          {token?.actor && isDM && (
            <div className="row">
              <HideButton token={token} command={command} />
              <button className="secondary" onClick={() => command("remove_token", { token: token.id })}>
                Remove from map
              </button>
            </div>
          )}
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
        </>
      )}

      {tab === "map" && isDM && (
        <>
          {game.map && <AddToken game={game} command={command} />}
          <MapPanel seat={seat} game={game} command={command} />
        </>
      )}
    </aside>
  );
}

function HideButton({ token, command }: { token: Token; command: Props["command"] }) {
  return (
    <button
      className="secondary"
      title={token.hidden ? "Let the players see it" : "Only you will see it"}
      onClick={() => command("set_token_hidden", { token: token.id, hidden: !token.hidden })}
    >
      {token.hidden ? "Reveal" : "Hide"}
    </button>
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
  const [actor, setActor] = useState("");
  const [label, setLabel] = useState("");
  const [color, setColor] = useState("#c0392b");
  const [size, setSize] = useState(1);
  const [controller, setController] = useState("");
  const [hidden, setHidden] = useState(false);
  const players = Object.values(game.members).filter((m) => m.role === "player");
  const unplaced = Object.values(game.actors).filter((a) => !tokenFor(game, a.id));

  function submit(e: FormEvent) {
    e.preventDefault();
    const at = freeCell(game, size);
    if (!at) return;
    if (actor) command("place_token", { actor, size, at, hidden });
    else command("place_token", { label, color, size, at, hidden, controllers: controller ? [controller] : [] });
    setLabel("");
    setActor("");
  }

  return (
    <section>
      <h3>Add token</h3>
      <form className="stack" onSubmit={submit}>
        <select value={actor} onChange={(e) => setActor(e.target.value)} aria-label="Character">
          <option value="">Plain token (no character)</option>
          {unplaced.map((a) => (
            <option key={a.id} value={a.id}>
              {a.name}
            </option>
          ))}
        </select>
        {!actor && (
          <div className="row">
            <input value={label} onChange={(e) => setLabel(e.target.value)} placeholder="Label" maxLength={32} required />
            <input type="color" value={color} onChange={(e) => setColor(e.target.value)} aria-label="Color" />
          </div>
        )}
        <div className="row">
          <select value={size} onChange={(e) => setSize(Number(e.target.value))} aria-label="Size">
            {SIZES.map((s) => (
              <option key={s.size} value={s.size}>
                {s.name}
              </option>
            ))}
          </select>
          {!actor && (
            <select value={controller} onChange={(e) => setController(e.target.value)} aria-label="Controlled by">
              <option value="">DM only</option>
              {players.map((m) => (
                <option key={m.user_id} value={m.user_id}>
                  {m.display_name}
                </option>
              ))}
            </select>
          )}
        </div>
        <label className="inline">
          <input type="checkbox" checked={hidden} onChange={(e) => setHidden(e.target.checked)} />
          Hidden from players
        </label>
        <button>Place</button>
      </form>
    </section>
  );
}

function MapPanel({ seat, game, command }: { seat: Seat; game: GameState; command: Props["command"] }) {
  const map = game.map;
  const [replacing, setReplacing] = useState(!map);
  return (
    <section className="stack">
      <h3>Map</h3>
      {map && (
        <>
          <GridForm key={map.id} map={map} command={command} />
          <label className="inline">
            <input type="checkbox" checked={map.fog.enabled} onChange={(e) => command("set_fog", { enabled: e.target.checked })} />
            Fog of war (reveal areas with the Reveal brush)
          </label>
          <label>
            Diagonals
            <select
              value={game.settings.diagonal}
              onChange={(e) => command("set_settings", { diagonal: e.target.value })}
            >
              <option value="5">Every diagonal 5 ft</option>
              <option value="5-10-5">Alternate 5 / 10 ft</option>
            </select>
          </label>
          {!replacing && (
            <button className="secondary" onClick={() => setReplacing(true)}>
              Replace map…
            </button>
          )}
        </>
      )}
      {replacing && <NewMap seat={seat} command={command} onDone={() => setReplacing(false)} canCancel={!!map} />}
    </section>
  );
}

// GridForm changes the grid of the current map, keeping painted terrain.
function GridForm({ map, command }: { map: MapInfo; command: Props["command"] }) {
  const [cols, setCols] = useState(map.cols);
  const [rows, setRows] = useState(map.rows);
  const [cellFeet, setCellFeet] = useState(map.cell_feet);
  const changed = cols !== map.cols || rows !== map.rows || cellFeet !== map.cell_feet;
  return (
    <form
      className="stack"
      onSubmit={(e) => {
        e.preventDefault();
        command("set_map", {
          image_url: map.image_url, background: map.background, cols, rows, cell_feet: cellFeet, keep_terrain: true,
        });
      }}
    >
      <GridFields cols={cols} rows={rows} cellFeet={cellFeet} setCols={setCols} setRows={setRows} setCellFeet={setCellFeet} />
      {changed && <button>Apply grid</button>}
    </form>
  );
}

function GridFields(p: {
  cols: number;
  rows: number;
  cellFeet: number;
  setCols(n: number): void;
  setRows(n: number): void;
  setCellFeet(n: number): void;
}) {
  return (
    <div className="row">
      <label>
        Columns
        <NumberInput min={1} max={200} value={p.cols} onChange={p.setCols} />
      </label>
      <label>
        Rows
        <NumberInput min={1} max={200} value={p.rows} onChange={p.setRows} />
      </label>
      <label>
        Feet/cell
        <NumberInput min={1} max={100} value={p.cellFeet} onChange={p.setCellFeet} />
      </label>
    </div>
  );
}

function NewMap(p: { seat: Seat; command: Props["command"]; onDone(): void; canCancel: boolean }) {
  const [kind, setKind] = useState<"image" | "blank">("image");
  const [file, setFile] = useState<File | null>(null);
  const [cols, setCols] = useState(20);
  const [rows, setRows] = useState(15);
  const [cellFeet, setCellFeet] = useState(5);
  const [background, setBackground] = useState("#e8e0cc");
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
    if (kind === "blank") {
      p.command("set_map", { background, cols, rows, cell_feet: cellFeet });
      p.onDone();
      return;
    }
    if (!file) return;
    setBusy(true);
    setError("");
    try {
      await uploadMap(p.seat, file, { cols, rows, cellFeet });
      p.onDone();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="stack" onSubmit={submit}>
      <div className="tabs" role="tablist">
        <button type="button" role="tab" aria-selected={kind === "image"} onClick={() => setKind("image")}>
          Upload image
        </button>
        <button type="button" role="tab" aria-selected={kind === "blank"} onClick={() => setKind("blank")}>
          Blank map
        </button>
      </div>
      {kind === "image" ? (
        <input type="file" accept="image/png,image/jpeg,image/webp,image/gif" onChange={(e) => void pick(e.target.files?.[0] ?? null)} required />
      ) : (
        <label className="inline">
          Background
          <input type="color" value={background} onChange={(e) => setBackground(e.target.value)} />
        </label>
      )}
      <GridFields cols={cols} rows={rows} cellFeet={cellFeet} setCols={setCols} setRows={setRows} setCellFeet={setCellFeet} />
      {error && <p className="error">{error}</p>}
      <div className="row">
        <button disabled={busy || (kind === "image" && !file)}>
          {busy ? "Uploading…" : kind === "image" ? "Upload" : "Create blank map"}
        </button>
        {p.canCancel && (
          <button type="button" className="secondary" onClick={p.onDone}>
            Cancel
          </button>
        )}
      </div>
    </form>
  );
}
