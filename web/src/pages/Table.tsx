import { useCallback, useEffect, useReducer, useRef, useState } from "react";
import { loadSeats, type Seat } from "../api";
import { MapCanvas, type Paint } from "../components/MapCanvas";
import { Sidebar } from "../components/Sidebar";
import { navigate } from "../nav";
import { Connection, type Status } from "../socket";
import { initialTable, reducer } from "../store";
import type { Cell, TokenID } from "../types";

export function Table({ sessionId }: { sessionId: string }) {
  const seat = loadSeats()[sessionId];
  if (!seat) {
    return (
      <main className="page">
        <div className="card stack">
          <h2>Not joined</h2>
          <p>This device hasn't joined that session. Open the invite link from your DM.</p>
          <button onClick={() => navigate("/")}>Home</button>
        </div>
      </main>
    );
  }
  return <TableView seat={seat} />;
}

// Cells per paint_cells command: a long stroke is sent in pieces, each well
// under the server's 16 KB message limit.
const PAINT_CHUNK = 500;

const STATUS_TEXT: Record<Status, string> = {
  connecting: "Connecting…",
  open: "Live",
  reconnecting: "Reconnecting…",
  gone: "Session not found",
};

function TableView({ seat }: { seat: Seat }) {
  const [table, dispatch] = useReducer(reducer, initialTable);
  const [status, setStatus] = useState<Status>("connecting");
  const [notice, setNotice] = useState("");
  const [selected, setSelected] = useState<TokenID | null>(null);
  const conn = useRef<Connection | null>(null);
  const seq = useRef(0); // the last event applied, for syncing after a reconnect
  seq.current = table.game?.seq ?? 0;

  useEffect(() => {
    const c = new Connection(seat.session, seat.token, {
      onMessage(msg) {
        switch (msg.type) {
          case "snapshot":
            dispatch({ type: "snapshot", state: msg.state });
            break;
          case "event": {
            const { type: _, ...event } = msg;
            dispatch({ type: "event", event });
            break;
          }
          case "events":
            dispatch({ type: "events", events: msg.events, seq: msg.seq });
            break;
          case "ack":
            dispatch({ type: "settle", id: msg.id });
            break;
          case "reject":
            dispatch({ type: "settle", id: msg.id });
            setNotice(msg.message);
            break;
          case "error":
            setNotice(msg.message);
            break;
        }
      },
      onStatus(s) {
        setStatus(s);
        if (s === "gone") dispatch({ type: "gone" });
      },
      lastSeq: () => seq.current,
    });
    conn.current = c;
    c.start();
    return () => c.stop();
  }, [seat.session, seat.token]);

  // An event arrived out of order: ask for a fresh snapshot.
  useEffect(() => {
    if (table.stale) conn.current?.sync();
  }, [table.stale]);

  useEffect(() => {
    if (!notice) return;
    const t = setTimeout(() => setNotice(""), 4000);
    return () => clearTimeout(t);
  }, [notice]);

  // A selected token can disappear (removed by the DM).
  const selectedToken = selected && table.game?.tokens[selected] ? selected : null;

  const command = useCallback((name: string, args: unknown) => {
    if (!conn.current?.command(name, args)) setNotice("Not connected. Try again in a moment.");
  }, []);

  const move = useCallback((token: TokenID, to: Cell) => {
    const id = conn.current?.command("move_token", { token, to });
    if (!id) return false;
    dispatch({ type: "move", id, move: { token, to } });
    return true;
  }, []);

  const paint = useCallback(
    (p: Paint) => {
      // Fog is painted like terrain, with its own commands.
      const fog = p.terrain === "reveal" || p.terrain === "cover";
      const name = fog ? (p.terrain === "reveal" ? "reveal_fog" : "hide_fog") : "paint_cells";
      const base = fog ? { for: p.for } : { terrain: p.terrain };
      if (p.rect) return command(name, { ...base, rect: p.rect });
      const cells = p.cells ?? [];
      for (let i = 0; i < cells.length; i += PAINT_CHUNK) {
        command(name, { ...base, cells: cells.slice(i, i + PAINT_CHUNK) });
      }
    },
    [command],
  );

  // The DM can look at the table as one of the players sees it.
  const [viewAs, setViewAs] = useState("");
  const viewing = (user: string) => {
    setViewAs(user);
    conn.current?.viewAs(user);
  };
  const isDM = seat.role === "dm";
  const players = Object.values(table.game?.members ?? {}).filter((m) => m.role === "player");

  useEffect(() => {
    if (seat.role !== "dm" || !selectedToken) return;
    const onKey = (e: KeyboardEvent) => {
      if ((e.key === "Delete" || e.key === "Backspace") && !(e.target instanceof HTMLInputElement)) {
        command("remove_token", { token: selectedToken });
      }
    };
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  }, [seat.role, selectedToken, command]);

  return (
    <div className="table">
      <header className="topbar">
        <a
          href="/"
          onClick={(e) => {
            e.preventDefault();
            navigate("/");
          }}
        >
          Yorm
        </a>
        <strong>{seat.name}</strong>
        {isDM && players.length > 0 && (
          <label className="inline view-as">
            View as
            <select value={viewAs} onChange={(e) => viewing(e.target.value)}>
              <option value="">DM (everything)</option>
              {players.map((m) => (
                <option key={m.user_id} value={m.user_id}>
                  {m.display_name}
                </option>
              ))}
            </select>
          </label>
        )}
        {viewAs && <span className="viewing">Seeing what {table.game?.members[viewAs]?.display_name} sees</span>}
        <span className={`status status-${status}`}>{STATUS_TEXT[status]}</span>
      </header>
      <MapCanvas
        table={table}
        me={viewAs || seat.user}
        isDM={isDM}
        dmView={isDM && !viewAs}
        selected={selectedToken}
        onSelect={setSelected}
        onMove={move}
        onPaint={paint}
      />
      {table.game && <Sidebar seat={seat} game={table.game} feed={table.feed} selected={selectedToken} command={command} />}
      {notice && (
        <div className="toast" role="status">
          {notice}
        </div>
      )}
    </div>
  );
}
