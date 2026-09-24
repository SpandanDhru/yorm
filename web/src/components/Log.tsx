import { useEffect, useRef, useState, type FormEvent, type KeyboardEvent } from "react";
import type { FeedLine } from "../store";
import type { GameState, Roll, TermResult } from "../types";

type Command = (name: string, args: unknown) => void;

const QUICK = [
  { label: "d20", text: "1d20" },
  { label: "Adv", text: "2d20kh1" },
  { label: "Dis", text: "2d20kl1" },
];

export function LogPanel({ game, feed, command }: { game: GameState; feed: FeedLine[]; command: Command }) {
  const [text, setText] = useState("");
  const [history, setHistory] = useState<string[]>([]);
  const [back, setBack] = useState(-1); // position while browsing history with the arrow keys
  const end = useRef<HTMLLIElement>(null);

  // Rolls come from the server's state; feed lines only from this visit.
  type Entry = { kind: "roll"; seq: number; roll: Roll } | { kind: "feed"; seq: number; text: string };
  const entries: Entry[] = [
    ...game.rolls.map((r): Entry => ({ kind: "roll", seq: r.seq, roll: r })),
    ...feed.map((f): Entry => ({ kind: "feed", seq: f.seq, text: f.text })),
  ].sort((a, b) => a.seq - b.seq);
  const last = entries.at(-1)?.seq;

  // Braces matter: newer browsers return a Promise from scrollIntoView,
  // which React would take for a cleanup function.
  useEffect(() => {
    end.current?.scrollIntoView({ block: "nearest" });
  }, [last]);

  function submit(e: FormEvent) {
    e.preventDefault();
    const line = text.replace(/^\s*\/r(oll)?\b/i, "").trim();
    if (!line) return;
    command("roll_dice", { text: line });
    setHistory((h) => [line, ...h.filter((x) => x !== line)].slice(0, 20));
    setBack(-1);
    setText("");
  }

  function keys(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key !== "ArrowUp" && e.key !== "ArrowDown") return;
    e.preventDefault();
    const i = Math.max(-1, Math.min(history.length - 1, back + (e.key === "ArrowUp" ? 1 : -1)));
    setBack(i);
    setText(i < 0 ? "" : history[i]!);
  }

  return (
    <section className="stack log">
      <ol className="log-lines">
        {entries.length === 0 && <li className="muted">No rolls yet.</li>}
        {entries.map((e) =>
          e.kind === "roll" ? (
            <li key={`r${e.seq}`}>
              <RollLine roll={e.roll} who={game.members[e.roll.by]?.display_name ?? "someone"} />
            </li>
          ) : (
            <li key={`f${e.seq}`} className="muted feed">
              {e.text}
            </li>
          ),
        )}
        <li ref={end} aria-hidden />
      </ol>
      <form className="stack" onSubmit={submit}>
        <div className="row">
          <input
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={keys}
            placeholder="/roll 1d20+5 attack"
            aria-label="Roll dice"
            maxLength={200}
          />
          <button disabled={!text.trim()}>Roll</button>
        </div>
        <div className="row quick">
          {QUICK.map((q) => (
            <button key={q.label} type="button" className="secondary small" onClick={() => setText(q.text + (text.match(/[+-]\d+$/)?.[0] ?? ""))}>
              {q.label}
            </button>
          ))}
        </div>
        <p className="muted hint">
          Rolled real dice? Add what you got: <code>2d6+3 = 4 5</code> (each die) or <code>1d20+5 = 17</code> (the total).
        </p>
      </form>
    </section>
  );
}

function RollLine({ roll, who }: { roll: Roll; who: string }) {
  return (
    <div className="roll">
      <div>
        <strong>{who}</strong> {roll.label && <span>{roll.label}: </span>}
        <span className="muted">{roll.expr}</span>
        {roll.physical && (
          <span title="Rolled on real dice" aria-label="physical roll">
            {" "}
            🎲
          </span>
        )}
      </div>
      <div className="roll-result">
        {roll.result.terms && <span className="roll-terms">{roll.result.terms.map((t, i) => <Term key={i} t={t} first={i === 0} />)}</span>}
        <span className="roll-total">{roll.result.total}</span>
      </div>
    </div>
  );
}

// Term shows a term's dice, striking through the ones not kept.
function Term({ t, first }: { t: TermResult; first: boolean }) {
  const sign = t.neg ? " − " : first ? "" : " + ";
  if (!t.faces) return <>{sign + (t.const ?? 0)}</>;
  return (
    <>
      {sign}[
      {t.faces.map((f, i) => (
        <span key={i}>
          {i > 0 && ", "}
          {t.dropped?.[i] ? <s>{f}</s> : f}
        </span>
      ))}
      ]
    </>
  );
}
