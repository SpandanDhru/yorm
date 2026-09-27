import "@fontsource/press-start-2p";
import { lazy, Suspense, useState, type FormEvent } from "react";
import { createSession, forgetSeat, loadSeats, saveSeat } from "../api";
import { AsciiTitle } from "../components/AsciiTitle";
import { navigate } from "../nav";

// Loaded on its own, so only the home page downloads three.js.
const Sword = lazy(() => import("../components/Sword"));

// "YORM" in the ANSI Shadow figlet font.
const TITLE = `██╗   ██╗ ██████╗ ██████╗ ███╗   ███╗
╚██╗ ██╔╝██╔═══██╗██╔══██╗████╗ ████║
 ╚████╔╝ ██║   ██║██████╔╝██╔████╔██║
  ╚██╔╝  ██║   ██║██╔══██╗██║╚██╔╝██║
   ██║   ╚██████╔╝██║  ██║██║ ╚═╝ ██║
   ╚═╝    ╚═════╝ ╚═╝  ╚═╝╚═╝     ╚═╝`;

export function Home() {
  const [name, setName] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [seats, setSeats] = useState(() => Object.values(loadSeats()));

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const seat = await createSession(name, displayName || "DM");
      saveSeat(seat);
      navigate(`/s/${seat.session}`);
    } catch (err) {
      setError((err as Error).message);
      setBusy(false);
    }
  }

  return (
    <main className="home">
      <header className="hero">
        <h1 className="ascii-title">
          <AsciiTitle art={TITLE} label="Yorm" />
        </h1>
        <p className="tagline">a shared battle grid for your table</p>
        <Suspense fallback={<div className="sword" />}>
          <Sword />
        </Suspense>
      </header>
      <div className="page">

      <form className="card stack" onSubmit={submit}>
        <h2>New session</h2>
        <label>
          Session name
          <input value={name} onChange={(e) => setName(e.target.value)} maxLength={80} required placeholder="The Sunless Citadel" />
        </label>
        <label>
          Your name
          <input value={displayName} onChange={(e) => setDisplayName(e.target.value)} maxLength={40} placeholder="DM" />
        </label>
        {error && <p className="error">{error}</p>}
        <button disabled={busy}>{busy ? "Creating…" : "Create session"}</button>
      </form>

      {seats.length > 0 && (
        <section className="card">
          <h2>Your sessions</h2>
          <ul className="list">
            {seats.map((s) => (
              <li key={s.session}>
                <a
                  href={`/s/${s.session}`}
                  onClick={(e) => {
                    e.preventDefault();
                    navigate(`/s/${s.session}`);
                  }}
                >
                  {s.name}
                </a>{" "}
                <span className="muted">{s.role === "dm" ? "DM" : "player"}</span>
                <button
                  className="icon"
                  title="Remove from this list (the session itself stays)"
                  aria-label={`Remove ${s.name} from this list`}
                  onClick={() => {
                    forgetSeat(s.session);
                    setSeats(Object.values(loadSeats()));
                  }}
                >
                  ×
                </button>
              </li>
            ))}
          </ul>
        </section>
      )}
      </div>
    </main>
  );
}
