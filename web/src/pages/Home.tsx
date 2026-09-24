import { useState, type FormEvent } from "react";
import { createSession, loadSeats, saveSeat } from "../api";
import { navigate } from "../nav";

export function Home() {
  const [name, setName] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const seats = Object.values(loadSeats());

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
    <main className="page">
      <h1>Yorm</h1>
      <p className="muted">A shared battle grid for your table.</p>

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
              </li>
            ))}
          </ul>
        </section>
      )}
    </main>
  );
}
