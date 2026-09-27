import { useState } from "react";
import type { Seat } from "../api";
import { canEdit } from "../store";
import { HIDDEN_ACTOR, type ActionKind, type Encounter, type GameState, type InitEntry } from "../types";
import { HPBar } from "./Characters";

type Command = (name: string, args: unknown) => void;

export function Initiative({ seat, game, command }: { seat: Seat; game: GameState; command: Command }) {
  const e = game.encounter;
  const isDM = seat.role === "dm";
  if (!e) return isDM ? <StartCombat game={game} command={command} /> : null;

  const active = e.active ? game.actors[e.active] : undefined;
  const waiting = e.order.filter((x) => x.total === null).length;
  const mayAct = !!active && canEdit(game, seat.user, active);

  return (
    <section className="initiative stack">
      <div className="row initiative-head">
        <h3>{e.active ? `Combat · round ${e.round}` : "Rolling initiative"}</h3>
        {isDM && (
          <button className="secondary small" onClick={() => confirm("End combat?") && command("end_combat", {})}>
            End
          </button>
        )}
      </div>

      <ol className="order">
        {e.order.map((entry) => (
          <OrderRow key={entry.actor} entry={entry} e={e} game={game} seat={seat} command={command} />
        ))}
      </ol>

      {!e.active && (
        <p className="muted">
          Waiting for {waiting} {waiting === 1 ? "roll" : "rolls"}.
          {isDM && (
            <>
              {" "}
              <button className="secondary small" onClick={() => command("begin_combat", {})}>
                Begin anyway
              </button>
            </>
          )}
        </p>
      )}

      {e.active === HIDDEN_ACTOR && (
        <div className="turn">
          <strong>A hidden combatant's turn</strong>
        </div>
      )}

      {active && (
        <div className="turn stack">
          <div className="row">
            <strong>{active.name}'s turn</strong>
            {!active.masked && <span className="muted">{e.economy.movement_left} ft left</span>}
          </div>
          <Resource label="Action" kind="action" available={e.economy.action} dashed={e.economy.action_dash} mayAct={mayAct} command={command} />
          <Resource label="Bonus action" kind="bonus" available={e.economy.bonus} dashed={e.economy.bonus_dash} mayAct={mayAct} command={command} />
          <div className="row">
            {mayAct && <button onClick={() => command("end_turn", {})}>End turn</button>}
            {isDM && (
              <button className="secondary" onClick={() => command("prev_turn", {})} title="Go back one turn">
                Back
              </button>
            )}
          </div>
        </div>
      )}

      {isDM && <AddCombatant game={game} e={e} command={command} />}
    </section>
  );
}

function OrderRow({ entry, e, game, seat, command }: { entry: InitEntry; e: Encounter; game: GameState; seat: Seat; command: Command }) {
  const a = game.actors[entry.actor];
  const [roll, setRoll] = useState("");
  if (!a) return null;
  const isDM = seat.role === "dm";
  const mine = canEdit(game, seat.user, a);
  const reactionUsed = !!e.reaction_used[a.id];
  const n = Number(roll);

  return (
    <li className={e.active === a.id ? "active" : undefined} aria-current={e.active === a.id ? "true" : undefined}>
      <span className="init-total" title={entry.physical ? "entered from a real die" : entry.roll ? `rolled ${entry.roll}` : undefined}>
        {entry.total ?? "–"}
        {entry.physical && " 🎲"}
      </span>
      <span className="init-name">
        {a.name}
        <HPBar character={a} />
      </span>
      {entry.total === null && mine ? (
        <form
          className="row"
          onSubmit={(ev) => {
            ev.preventDefault();
            if (n >= 1 && n <= 20) command("set_initiative", { actor: a.id, roll: n });
          }}
        >
          <input
            className="d20"
            inputMode="numeric"
            autoComplete="off"
            value={roll}
            onChange={(ev) => /^\d{0,2}$/.test(ev.target.value) && setRoll(ev.target.value)}
            placeholder="d20"
            aria-label={`${a.name}'s d20 roll`}
          />
          <button className="small">Enter</button>
        </form>
      ) : entry.total === null ? (
        <span className="muted">waiting</span>
      ) : (
        e.active && (
          <button
            className={`chip-toggle${reactionUsed ? " used" : ""}`}
            disabled={!mine}
            title={reactionUsed ? "Reaction used" : "Reaction available"}
            onClick={() => command("use_action", { actor: a.id, kind: "reaction", used: !reactionUsed })}
          >
            Reaction
          </button>
        )
      )}
      {isDM && e.active !== a.id && (
        <button className="icon" aria-label={`Remove ${a.name} from combat`} onClick={() => command("remove_from_combat", { actor: a.id })}>
          ×
        </button>
      )}
    </li>
  );
}

function Resource(p: { label: string; kind: ActionKind; available: boolean; dashed: boolean; mayAct: boolean; command: Command }) {
  const use = (args: object) => p.command("use_action", { kind: p.kind, ...args });
  if (!p.available) {
    return (
      <div className="resource used">
        <span>
          {p.label}: {p.dashed ? "Dashed" : "used"}
        </span>
        {p.mayAct && (
          <button className="small secondary" onClick={() => use({ used: false })}>
            Undo
          </button>
        )}
      </div>
    );
  }
  return (
    <div className="resource">
      <span>{p.label}</span>
      {p.mayAct && (
        <span className="row">
          <button className="small" onClick={() => use({})}>
            Use
          </button>
          <button className="small secondary" onClick={() => use({ dash: true })} title="Dash: add your speed to this turn's movement">
            Dash
          </button>
        </span>
      )}
    </div>
  );
}

function StartCombat({ game, command }: { game: GameState; command: Command }) {
  const onMap = Object.values(game.tokens).filter((t) => t.actor && game.actors[t.actor]).length;
  return (
    <section className="stack">
      <button disabled={onMap === 0} onClick={() => command("start_combat", {})} title="Everyone with a token on the map rolls initiative">
        Start combat{onMap > 0 && ` (${onMap} on the map)`}
      </button>
    </section>
  );
}

function AddCombatant({ game, e, command }: { game: GameState; e: Encounter; command: Command }) {
  const out = Object.values(game.actors).filter((a) => !e.order.some((x) => x.actor === a.id));
  const [pick, setPick] = useState("");
  if (out.length === 0) return null;
  return (
    <form
      className="row"
      onSubmit={(ev) => {
        ev.preventDefault();
        if (pick) command("join_combat", { actor: pick });
        setPick("");
      }}
    >
      <select value={pick} onChange={(ev) => setPick(ev.target.value)} aria-label="Add to combat">
        <option value="">Add to combat…</option>
        {out.map((a) => (
          <option key={a.id} value={a.id}>
            {a.name}
          </option>
        ))}
      </select>
      <button className="secondary" disabled={!pick}>
        Add
      </button>
    </form>
  );
}
