import { useState, type FormEvent } from "react";
import { joinSession, loadSeats, saveSeat } from "../api";
import { navigate } from "../nav";

export function Join({ sessionId }: { sessionId: string }) {
  const code = location.hash.slice(1);
  const existing = loadSeats()[sessionId];
  const [displayName, setDisplayName] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const seat = await joinSession(sessionId, code, displayName);
      saveSeat(seat);
      navigate(`/s/${seat.session}`);
    } catch (err) {
      setError((err as Error).message);
      setBusy(false);
    }
  }

  if (existing) {
    return (
      <main className="page">
        <div className="card stack">
          <h2>{existing.name}</h2>
          <p>You already joined this session on this device.</p>
          <button onClick={() => navigate(`/s/${sessionId}`)}>Go to the table</button>
        </div>
      </main>
    );
  }

  return (
    <main className="page">
      <form className="card stack" onSubmit={submit}>
        <h2>Join session</h2>
        {!code && <p className="error">This invite link is missing its code. Ask the DM for the full link.</p>}
        <label>
          Your name
          <input value={displayName} onChange={(e) => setDisplayName(e.target.value)} maxLength={40} required autoFocus />
        </label>
        {error && <p className="error">{error}</p>}
        <button disabled={busy || !code}>{busy ? "Joining…" : "Join"}</button>
      </form>
    </main>
  );
}
