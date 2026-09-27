# Key decisions

The decisions that shaped Yorm, why each was made, and what it cost. Each one ends with the question an interviewer is likely to ask about it.

## The big picture

**The server is authoritative.** Browsers send *commands* ("move token X to (4, 7)"). The server checks them against the rules, and only the server changes the state. The browser never decides whether a move is legal.
- *Why:* players can't cheat by editing their browser's JavaScript, and hidden information (fog, hidden monsters, secret rolls) can be kept on the server instead of hidden in the UI.
- *Cost:* every action takes a round trip to the server. That's fine for a turn-based game, where 50 ms isn't noticeable.
- *Q: Why not let clients update the state and sync (like a CRDT)?* Because a game has rules and secrets. CRDTs merge concurrent edits; they don't enforce "you can only move 30 ft" or "players can't see this goblin."

**Go for the server, React + Konva for the browser.** Go has cheap goroutines and channels, which suit thousands of long-lived WebSocket connections and one actor per session. Konva draws the grid, tokens and drawings on a canvas; React handles the panels around it.

## Milestone 0–1: the core loop

**Event sourcing.** Postgres doesn't store the current state. It stores an append-only log of *events* ("TokenMoved", "DamageDealt"). The state is whatever you get by applying every event in order.
- *Why:* a full history for free (the dice log and the game log *are* the event log), crash recovery by replaying, and an easy way to test that "replay equals live."
- *Cost:* more machinery than a `tokens` table: snapshots, event versioning, and replay.
- *Q: How do you change an event's shape later?* Every stored event has a `version`. When the shape changes, you bump the version and write an *upcaster* that converts the old JSON to the new shape on load (`internal/game/events.go`). Old rows are never rewritten.

**Decide / Apply split.** `game.Decide(state, command)` validates and returns events; `Apply(state, event)` changes the state. `Apply` is pure: no clock, no randomness, no I/O. Dice are rolled in `Decide`, and the result is stored in the event.
- *Why:* replaying the log always gives exactly the same state. If `Apply` rolled dice, a replay would roll different numbers.
- *Q: Where does randomness live?* In `Decide`, and it's recorded in the event. Event timestamps are truncated to microseconds, because Postgres stores microseconds: a nanosecond time in memory would differ from the replayed one. A real bug the property tests caught.

**One actor (goroutine) per session.** Each game session is owned by a single goroutine with an inbox channel. It handles commands one at a time.
- *Why:* no locks on the game state, and every client sees events in the same order. Sessions are independent, so they run in parallel across cores.
- *Cost:* one session can't use more than one core. A D&D table is 6 people, so that's never the limit.
- *Q: What happens with two commands at once?* They queue in the inbox; the second is checked against the state after the first. Two players moving into the same square: the second is rejected.

**Persist, then apply, then broadcast.** The actor appends the events to Postgres and waits for the commit. Only then does it apply them and send them to clients.
- *Why:* nobody ever sees something that wasn't saved. If the server dies right after a broadcast, the event is already in the log.
- *Cost:* every command waits for a disk flush. That's the latency floor (see group commit below).

**Two actors can't both write one session.** Each append says "I expect `last_seq` to be N" in a single SQL statement, and it inserts nothing if someone else moved it. The primary key `(session_id, seq)` is a second guard. A stale actor that loses the race shuts itself down.
- *Q: How would you run more than one server?* This guard makes it safe, but you'd also need routing so each session's clients reach the machine that owns its actor (for example consistent hashing, or a lease table). Today it's one machine on purpose.

**Signed join tokens, no accounts.** Joining with an invite code gives you a token: your user ID and role, signed with HMAC-SHA256 using a server secret (`internal/auth`). The token is the identity.
- *Why:* no signup friction for a game night, and nothing to store per login.
- *Cost:* you can't revoke one token without changing the secret, which signs everyone out. Losing your browser's storage loses your seat.

## Milestone 2: rules

- **Movement is checked on the server.** It uses the grid, the terrain (walls block; difficult terrain and water cost double) and the diagonal rule. Diagonals are 5 ft by default, with 5-10-5 as a per-session option, because tables play both ways. The DM can override.
- **Physical dice.** Players can type in a real roll instead of the server rolling. It's marked "physical" in the log, so it's honest about where the number came from.
- **Blank maps and terrain painting** were added to the spec: many DMs don't have map images.

## Milestone 3: resilience

**Snapshots.** Every 200 events or 5 minutes, the actor saves the whole state. On start, it loads the newest snapshot plus the events after it. The newest 3 snapshots are kept.
- *Result:* a 5,000-event session restores in about 5 ms, against a 200 ms target.
- *Q: Why keep 3?* So a bad snapshot, for example from a bug, isn't the only one.

**Reconnect and catch-up.** A client remembers the sequence number of the last event it applied. On reconnect it sends that number and gets only what it missed, from a ring buffer of the last 500 events. If it's too far behind, it gets a fresh snapshot of its view instead.

**Idempotent commands.** Every command has an ID. The actor remembers the answer to the last 1,000 commands (and saves them in snapshots). A command resent after a reconnect gets the same answer again and is never applied twice.
- *Q: What's the difference between at-least-once and exactly-once here?* The browser sends at least once (it resends anything unanswered); deduplicating by command ID makes the *effect* happen exactly once.

**Slow clients don't block anyone.** Each connection has a reader goroutine, a writer goroutine and a bounded send buffer. If a client's buffer fills up, that client is disconnected (it will reconnect and catch up) instead of the actor waiting on it. Each client is also limited to 20 commands a second (a token bucket).

## Milestone 4: hidden information

**The server filters what each player receives.** The DM gets every event. For each player, `game.Project` either passes the event through, strips the hidden details (a hidden monster's position, a secret roll's result), or sends a bare placeholder. The placeholder keeps sequence numbers continuous, so gap detection still works.
- *Why:* hiding things only in the UI means anyone who opens the browser dev tools sees them. Here, the data never reaches the browser.
- *Fog of war* is stored per map as a bitset (one bit per cell, base64 in JSON). When fog lifts or a token is revealed, the player gets a fresh full view (`State.View`) rather than trying to patch in everything newly visible.
- **Monster HP for players** is coarse: healthy, bloodied (at or below half), or down. NPCs are treated like monsters.
- *The strongest test:* for every player, rebuild the state from only the messages they were sent. It must equal their view and contain nothing hidden. This is checked over thousands of random games.

## Milestone 5: performance

**Load test first, then fix what it shows.** The first full-size run (500 sessions × 6 bots = 3,000 WebSockets, about 1,000 commands a second) had a p99 of **11.8 seconds**. Metrics showed the time was spent waiting on Postgres commits, not on CPU.
1. **One SQL statement per append** instead of four round trips (`BEGIN`, update, insert, `COMMIT`). It's a data-modifying CTE that checks `last_seq` and inserts in the same statement. That brought p99 to about 10 ms.
2. **A bigger connection pool made it worse** (27 ms with 32 connections vs 10 ms with 12). More concurrent commits just fight over Postgres's write-ahead-log lock. Worth remembering: more parallelism isn't free.
3. **Group commit.** A few writer goroutines batch whatever appends are queued, across sessions, into one transaction: one disk flush for many commands. Under a slow disk the batches grow instead of the queue.
- *Result:* on a GitHub-hosted VM, p50 1.9 ms and p99 38 ms (target: 50 ms), with 0 lost events and 0 clients out of sync. The server used about 0.6 CPU cores.
- *Q: What's your bottleneck?* The disk flush per commit (fsync), which is why batching commits helps more than adding connections.

**Observability.** Prometheus metrics (append time, batch size, connections, commands per second) with a Grafana dashboard in Docker Compose. The 11.8 s problem was diagnosed from these metrics, not by guessing.

## Milestone 6: testing

The tests are layered by what each kind can catch:
- **Unit:** rules like movement cost and turn order.
- **Property:** random games, checking invariants that always hold, e.g. HP stays in range and replay equals live.
- **Fuzz:** random bytes as commands, stored events, dice strings and tokens, checking for no crashes.
- **Integration:** real Postgres (testcontainers) and real WebSockets.
- **Chaos:** `kill -9` the real server mid-game and restart it. Every acknowledged command must be in the log, none applied twice, and every bot's state must match.
- **Load:** the bots measure latency and compare a hash of their state against the server's.
- *Q: What's the most valuable test?* The property tests. They found the timestamp-precision bug and a fog leak (one player's fog reveals being sent to another) that hand-written tests had missed.

## After the milestones

- **Uploads** (map images, token pictures) are stored on disk under a folder per session, so deleting a session deletes its files. File access goes through Go's `os.Root`, which blocks path tricks like `../../etc/passwd`.
- **DM drawing** is stored as events like everything else, so it replays and survives restarts.
- **Styled confirm dialogs** use the native `<dialog>` element, which traps focus and closes on Esc, instead of `window.confirm`.

## Deployment

**Fly.io for the server, Neon for Postgres.**
- **Why not GitHub Pages or Vercel:** those host static files or short-lived functions. Yorm needs a process that stays running to hold the session actors and the open WebSockets.
- **One machine, on purpose.** Sessions live in that machine's memory, and there's no routing between machines yet.
- **The machine sleeps when idle** and wakes on the next request, which keeps it cheap.
- **Uploads live on a 1 GB Fly volume**, with daily snapshots.
- **Neon's direct connection, not its pooler:** pgx uses prepared statements, which a transaction-mode pooler (PgBouncer) breaks.
- **A separate `/livez` health check** answers without touching the database. Fly checks health every 30 s; using `/healthz`, which pings Postgres, would keep Neon's serverless database awake and billing.
- **Migrations run on startup** (goose, with the SQL embedded in the binary), so a deploy is a single `fly deploy`.

## Limits and what I'd do next

- **Scale out:** route each session to an owning machine (consistent hashing or a lease table in Postgres). The `last_seq` guard already makes a mistake here safe, just not fast.
- **Accounts:** real logins would allow revoking access and using multiple devices.
- **Object storage (S3/R2) for uploads** instead of a volume, so any machine could serve them.
- **Log compaction:** very long campaigns grow the event table forever. Old events before the newest snapshot could be archived.
