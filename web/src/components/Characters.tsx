import { useState, type FormEvent } from "react";
import type { Seat } from "../api";
import { canEdit, freeCell, hpFraction, hpLevel, tokenFor } from "../store";
import type { ActorID, ActorKind, Character, GameState } from "../types";
import { NumberInput } from "./NumberInput";

type Command = (name: string, args: unknown) => void;

const KIND_LABEL: Record<ActorKind, string> = { pc: "PC", npc: "NPC", monster: "Monster" };
const HP_LABEL = { healthy: "Healthy", bloodied: "Bloodied", down: "Down" };

// The 5e conditions, offered as suggestions; anything else is allowed too.
const CONDITIONS = [
  "Blinded", "Charmed", "Deafened", "Exhaustion", "Frightened", "Grappled", "Incapacitated", "Invisible",
  "Paralyzed", "Petrified", "Poisoned", "Prone", "Restrained", "Stunned", "Unconscious", "Concentrating",
];

export function CharactersPanel(p: { seat: Seat; game: GameState; command: Command; focus: ActorID | null }) {
  const { seat, game, command } = p;
  const isDM = seat.role === "dm";
  const [open, setOpen] = useState<ActorID | null>(null);
  const [creating, setCreating] = useState(false);
  const shown = p.focus ?? open;
  const order: Record<ActorKind, number> = { pc: 0, npc: 1, monster: 2 };
  const actors = Object.values(game.actors).sort((a, b) => order[a.kind] - order[b.kind] || a.name.localeCompare(b.name));
  const hasOwn = actors.some((a) => a.kind === "pc" && a.controllers.includes(seat.user));

  return (
    <section className="stack">
      {actors.length === 0 && !creating && <p className="muted">No characters yet.</p>}
      <ul className="cards">
        {actors.map((a) => (
          <li key={a.id}>
            <CharacterCard
              character={a}
              game={game}
              seat={seat}
              command={command}
              expanded={shown === a.id}
              onToggle={() => setOpen(shown === a.id ? null : a.id)}
            />
          </li>
        ))}
      </ul>
      {creating ? (
        <CharacterForm
          isDM={isDM}
          game={game}
          onCancel={() => setCreating(false)}
          onSubmit={(fields) => {
            command("create_character", fields);
            setCreating(false);
          }}
        />
      ) : (
        (isDM || !hasOwn) && (
          <button className="secondary" onClick={() => setCreating(true)}>
            {isDM ? "New character" : "Create your character"}
          </button>
        )
      )}
    </section>
  );
}

function CharacterCard(p: {
  character: Character;
  game: GameState;
  seat: Seat;
  command: Command;
  expanded: boolean;
  onToggle(): void;
}) {
  const { character: a, game, seat, command } = p;
  const isDM = seat.role === "dm";
  const editable = canEdit(game, seat.user, a);
  const [editing, setEditing] = useState(false);
  const [amount, setAmount] = useState("");
  const [condition, setCondition] = useState("");
  const n = Number(amount);
  const validAmount = Number.isInteger(n) && n > 0;
  const hp = (name: string, args: object) => {
    command(name, { actor: a.id, ...args });
    setAmount("");
  };

  if (editing) {
    return (
      <CharacterForm
        initial={a}
        isDM={isDM}
        game={game}
        onCancel={() => setEditing(false)}
        onSubmit={(fields) => {
          command("update_character", { actor: a.id, ...fields });
          setEditing(false);
        }}
      />
    );
  }

  return (
    <div className={`card-character${p.expanded ? " open" : ""}`}>
      <button className="card-head" onClick={p.onToggle} aria-expanded={p.expanded}>
        <span className="card-name">
          {a.name}
          <span className={`tag tag-${a.kind}`}>{KIND_LABEL[a.kind]}</span>
        </span>
        <span className="card-stats">
          {a.masked ? (
            HP_LABEL[hpLevel(a)]
          ) : (
            <>
              AC {a.ac} · {a.hp.current}/{a.hp.max}
              {a.hp.temp > 0 && ` +${a.hp.temp}`}
            </>
          )}
        </span>
        <HPBar character={a} />
      </button>

      {p.expanded && (
        <div className="card-body stack">
          {a.masked ? (
            <p className="muted">The DM keeps this one's stats to themselves.</p>
          ) : (
            <p className="muted">
              {[a.class, a.level > 0 && `level ${a.level}`].filter(Boolean).join(", ") || KIND_LABEL[a.kind]} · speed {a.speed} ft ·
              init {a.init_bonus >= 0 ? `+${a.init_bonus}` : a.init_bonus}
              {a.rolls_own_dice && " · rolls own dice"}
            </p>
          )}

          {editable && (
            <div className="row hp-row">
              <input
                inputMode="numeric"
                autoComplete="off"
                value={amount}
                onChange={(e) => /^\d{0,4}$/.test(e.target.value) && setAmount(e.target.value)}
                placeholder="Amount"
                aria-label="HP amount"
              />
              <button className="danger" disabled={!validAmount} onClick={() => hp("adjust_hp", { delta: -n })}>
                Damage
              </button>
              <button disabled={!validAmount} onClick={() => hp("adjust_hp", { delta: n })}>
                Heal
              </button>
              <button className="secondary" disabled={!validAmount} title="Temporary HP" onClick={() => hp("set_temp_hp", { temp: n })}>
                Temp
              </button>
            </div>
          )}

          <div className="chips">
            {a.conditions.map((c) => (
              <span key={c.name} className="chip" title={c.source ? `from ${c.source}` : undefined}>
                {c.name}
                {editable && (
                  <button aria-label={`Remove ${c.name}`} onClick={() => command("remove_condition", { actor: a.id, name: c.name })}>
                    ×
                  </button>
                )}
              </span>
            ))}
            {a.conditions.length === 0 && <span className="muted">No conditions</span>}
          </div>
          {editable && (
            <form
              className="row"
              onSubmit={(e) => {
                e.preventDefault();
                if (!condition.trim()) return;
                command("add_condition", { actor: a.id, name: condition });
                setCondition("");
              }}
            >
              <input list="conditions" value={condition} onChange={(e) => setCondition(e.target.value)} placeholder="Add condition" maxLength={32} />
              <datalist id="conditions">
                {CONDITIONS.map((c) => (
                  <option key={c} value={c} />
                ))}
              </datalist>
              <button className="secondary">Add</button>
            </form>
          )}

          {editable && (
            <div className="row">
              <button className="secondary" onClick={() => setEditing(true)}>
                Edit
              </button>
              {isDM && game.map && !tokenFor(game, a.id) && (
                <>
                  <button className="secondary" onClick={() => command("place_token", { actor: a.id, at: freeCell(game, 1) })}>
                    Put on map
                  </button>
                  <button
                    className="secondary"
                    title="Only you see it until you reveal it"
                    onClick={() => command("place_token", { actor: a.id, at: freeCell(game, 1), hidden: true })}
                  >
                    Put on map hidden
                  </button>
                </>
              )}
              {isDM && (
                <button
                  className="secondary"
                  onClick={() => {
                    if (confirm(`Delete ${a.name}? Their token is removed too.`)) command("delete_character", { actor: a.id });
                  }}
                >
                  Delete
                </button>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

export function HPBar({ character: a }: { character: Character }) {
  const pct = hpFraction(a) * 100;
  const level = hpLevel(a);
  return (
    <span className="hpbar" role="meter" aria-valuemin={0} aria-valuemax={a.hp.max} aria-valuenow={a.hp.current} aria-label="Hit points">
      <span className={`hpbar-fill hp-${level}`} style={{ width: `${pct}%` }} />
    </span>
  );
}

interface Fields {
  kind?: ActorKind;
  name: string;
  class: string;
  level: number;
  ac: number;
  speed: number;
  init_bonus: number;
  max_hp: number;
  controllers?: string[];
  rolls_own_dice: boolean;
}

function CharacterForm(p: { initial?: Character; isDM: boolean; game: GameState; onSubmit(f: Fields): void; onCancel(): void }) {
  const i = p.initial;
  const [f, setF] = useState<Fields>({
    kind: i?.kind ?? (p.isDM ? "monster" : "pc"),
    name: i?.name ?? "",
    class: i?.class ?? "",
    level: i?.level ?? 1,
    ac: i?.ac ?? 10,
    speed: i?.speed ?? 30,
    init_bonus: i?.init_bonus ?? 0,
    max_hp: i?.hp.max ?? 10,
    controllers: i?.controllers ?? [],
    rolls_own_dice: i?.rolls_own_dice ?? false,
  });
  const set = <K extends keyof Fields>(k: K, v: Fields[K]) => setF((old) => ({ ...old, [k]: v }));
  const players = Object.values(p.game.members).filter((m) => m.role === "player");

  function submit(e: FormEvent) {
    e.preventDefault();
    // Only the DM may send kind and controllers.
    const { kind, controllers, ...rest } = f;
    p.onSubmit(p.isDM ? { kind, controllers, ...rest } : rest);
  }

  const num = (k: "level" | "ac" | "speed" | "init_bonus" | "max_hp", label: string, min: number, max: number) => (
    <label>
      {label}
      <NumberInput min={min} max={max} value={f[k]} onChange={(n) => set(k, n)} required />
    </label>
  );

  return (
    <form className="card-character open card-body stack" onSubmit={submit}>
      <h3>{i ? `Edit ${i.name}` : "New character"}</h3>
      <div className="row">
        <label>
          Name
          <input value={f.name} onChange={(e) => set("name", e.target.value)} maxLength={32} required autoFocus />
        </label>
        {p.isDM && (
          <label>
            Kind
            <select value={f.kind} onChange={(e) => set("kind", e.target.value as ActorKind)}>
              <option value="pc">PC</option>
              <option value="npc">NPC</option>
              <option value="monster">Monster</option>
            </select>
          </label>
        )}
      </div>
      <div className="row">
        <label>
          Class or type
          <input value={f.class} onChange={(e) => set("class", e.target.value)} maxLength={32} />
        </label>
        {num("level", "Level", 0, 30)}
      </div>
      <div className="row">
        {num("ac", "AC", 0, 40)}
        {num("max_hp", "Max HP", 1, 9999)}
      </div>
      <div className="row">
        {num("speed", "Speed", 0, 200)}
        {num("init_bonus", "Init bonus", -10, 20)}
      </div>
      {p.isDM && (
        <label>
          Played by
          <select
            value={f.controllers?.[0] ?? ""}
            onChange={(e) => set("controllers", e.target.value ? [e.target.value] : [])}
          >
            <option value="">The DM</option>
            {players.map((m) => (
              <option key={m.user_id} value={m.user_id}>
                {m.display_name}
              </option>
            ))}
          </select>
        </label>
      )}
      <label className="inline">
        <input type="checkbox" checked={f.rolls_own_dice} onChange={(e) => set("rolls_own_dice", e.target.checked)} />
        Rolls own dice (enter physical rolls)
      </label>
      <div className="row">
        <button>{i ? "Save" : "Create"}</button>
        <button type="button" className="secondary" onClick={p.onCancel}>
          Cancel
        </button>
      </div>
    </form>
  );
}
