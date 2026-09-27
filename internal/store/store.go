// Package store persists sessions, members, and each session's append-only
// event log in Postgres.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SpandanDhru/yorm/internal/game"
	"github.com/SpandanDhru/yorm/internal/metrics"
)

var (
	ErrNotFound = errors.New("store: not found")
	// ErrConflict means another writer already appended at these sequence
	// numbers, so the caller's view of the log is stale.
	ErrConflict = errors.New("store: sequence conflict")
)

type Session struct {
	ID         string
	Name       string
	InviteCode string
	CreatedAt  time.Time
}

type Postgres struct {
	pool *pgxpool.Pool

	// With group commit on, appends queue here for the writers.
	queue   chan *appendReq
	writers sync.WaitGroup
}

func New(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool}
}

// CreateSession inserts the session and its DM in one transaction. The
// DM's MemberJoined is the session's first event, so the DM can act (say,
// upload a map) before ever connecting.
func (p *Postgres) CreateSession(ctx context.Context, s Session, dm game.Member) error {
	return pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO sessions (id, name, invite_code) VALUES ($1, $2, $3)`,
			s.ID, s.Name, s.InviteCode); err != nil {
			return fmt.Errorf("store: create session: %w", err)
		}
		if err := insertMember(ctx, tx, s.ID, dm); err != nil {
			return err
		}
		first := game.Event{Seq: 1, Name: "MemberJoined", By: dm.UserID, At: time.Now().UTC(), Data: game.MemberJoined{Member: dm}}
		if err := appendTx(ctx, tx, s.ID, []game.Event{first}); err != nil {
			return fmt.Errorf("store: create session: %w", err)
		}
		return nil
	})
}

func (p *Postgres) Session(ctx context.Context, id string) (Session, error) {
	s := Session{ID: id}
	err := p.pool.QueryRow(ctx,
		`SELECT name, invite_code, created_at FROM sessions WHERE id = $1`, id,
	).Scan(&s.Name, &s.InviteCode, &s.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("store: session: %w", err)
	}
	return s, nil
}

// DeleteSession removes a session and everything recorded for it, or
// returns ErrNotFound.
func (p *Postgres) DeleteSession(ctx context.Context, id string) error {
	err := pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		for _, table := range []string{"events", "snapshots", "members"} {
			if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE session_id = $1`, id); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, id)
		if err == nil && tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return err
	})
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store: delete session: %w", err)
	}
	return err
}

func (p *Postgres) AddMember(ctx context.Context, sessionID string, m game.Member) error {
	return insertMember(ctx, p.pool, sessionID, m)
}

func insertMember(ctx context.Context, db interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, sessionID string, m game.Member) error {
	_, err := db.Exec(ctx,
		`INSERT INTO members (session_id, user_id, display_name, role) VALUES ($1, $2, $3, $4)`,
		sessionID, m.UserID, m.DisplayName, m.Role)
	if err != nil {
		return fmt.Errorf("store: add member: %w", err)
	}
	return nil
}

func (p *Postgres) Member(ctx context.Context, sessionID string, user game.UserID) (game.Member, error) {
	m := game.Member{UserID: user}
	err := p.pool.QueryRow(ctx,
		`SELECT display_name, role FROM members WHERE session_id = $1 AND user_id = $2`, sessionID, user,
	).Scan(&m.DisplayName, (*string)(&m.Role))
	if errors.Is(err, pgx.ErrNoRows) {
		return game.Member{}, ErrNotFound
	}
	if err != nil {
		return game.Member{}, fmt.Errorf("store: member: %w", err)
	}
	if !m.Role.Valid() {
		return game.Member{}, fmt.Errorf("store: member %s has unknown role %q", user, m.Role)
	}
	return m, nil
}

// LoadAfter returns the session's events with seq greater than after, in
// order, or ErrNotFound if the session does not exist.
func (p *Postgres) LoadAfter(ctx context.Context, sessionID string, after int64) ([]game.Event, error) {
	return p.events(ctx, sessionID, after, 0)
}

// Events returns up to limit events with seq greater than after, for the
// history API.
func (p *Postgres) Events(ctx context.Context, sessionID string, after int64, limit int) ([]game.Event, error) {
	return p.events(ctx, sessionID, after, limit)
}

// events loads events after a seq; limit 0 means all of them.
func (p *Postgres) events(ctx context.Context, sessionID string, after int64, limit int) ([]game.Event, error) {
	var exists bool
	if err := p.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sessions WHERE id = $1)`, sessionID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("store: load: %w", err)
	}
	if !exists {
		return nil, ErrNotFound
	}
	q := `SELECT seq, name, by_user, coalesce(cause, ''), data, version, at FROM events
		WHERE session_id = $1 AND seq > $2 ORDER BY seq`
	args := []any{sessionID, after}
	if limit > 0 {
		q += ` LIMIT $3`
		args = append(args, limit)
	}
	rows, err := p.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: load: %w", err)
	}
	defer rows.Close()
	evs := []game.Event{}
	for rows.Next() {
		var ev game.Event
		var data []byte
		var version int16
		if err := rows.Scan(&ev.Seq, &ev.Name, &ev.By, &ev.Cause, &data, &version, &ev.At); err != nil {
			return nil, fmt.Errorf("store: load: %w", err)
		}
		ev.At = ev.At.UTC() // pgx returns local time; events are UTC everywhere else
		if ev.Data, err = game.DecodePayload(ev.Name, int(version), data); err != nil {
			return nil, fmt.Errorf("store: load seq %d: %w", ev.Seq, err)
		}
		evs = append(evs, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: load: %w", err)
	}
	return evs, nil
}

// KeepSnapshots is how many snapshots a session keeps; older ones are
// deleted when a new one is saved.
const KeepSnapshots = 3

// SaveSnapshot stores a session's state as of seq, in the given format.
func (p *Postgres) SaveSnapshot(ctx context.Context, sessionID string, seq int64, format int, state []byte) error {
	err := pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO snapshots (session_id, seq, format, state) VALUES ($1, $2, $3, $4)
			 ON CONFLICT (session_id, seq) DO NOTHING`,
			sessionID, seq, format, state); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`DELETE FROM snapshots WHERE session_id = $1 AND seq NOT IN (
				SELECT seq FROM snapshots WHERE session_id = $1 ORDER BY seq DESC LIMIT $2)`,
			sessionID, KeepSnapshots)
		return err
	})
	if err != nil {
		return fmt.Errorf("store: save snapshot: %w", err)
	}
	return nil
}

// LatestSnapshot returns the newest snapshot in the given format, or
// ErrNotFound if there is none.
func (p *Postgres) LatestSnapshot(ctx context.Context, sessionID string, format int) (seq int64, state []byte, err error) {
	err = p.pool.QueryRow(ctx,
		`SELECT seq, state FROM snapshots WHERE session_id = $1 AND format = $2 ORDER BY seq DESC LIMIT 1`,
		sessionID, format,
	).Scan(&seq, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, ErrNotFound
	}
	if err != nil {
		return 0, nil, fmt.Errorf("store: latest snapshot: %w", err)
	}
	return seq, state, nil
}

// Append stores evs, which must have consecutive seqs following the
// session's last one, in a single insert. If another writer got there first
// it returns ErrConflict and stores nothing.
func (p *Postgres) Append(ctx context.Context, sessionID string, evs []game.Event) error {
	if len(evs) == 0 {
		return nil
	}
	var err error
	if p.queue == nil {
		err = appendTx(ctx, p.pool, sessionID, evs)
	} else {
		err = p.appendGrouped(ctx, sessionID, evs)
	}
	if err != nil && !errors.Is(err, ErrConflict) {
		return fmt.Errorf("store: append: %w", err)
	}
	return err
}

// GroupCommit makes appends share transactions: writers goroutines each
// take whatever appends are queued, from any sessions, up to maxBatch,
// and commit them together, so one disk flush covers many commands. When
// load is light a batch is a single append, so latency doesn't grow.
// Call Close to stop the writers.
func (p *Postgres) GroupCommit(writers, maxBatch int) {
	p.queue = make(chan *appendReq, writers*maxBatch)
	for range writers {
		p.writers.Add(1)
		go func() {
			defer p.writers.Done()
			p.writer(maxBatch)
		}()
	}
}

// Close stops the group-commit writers after they finish what's queued.
func (p *Postgres) Close() {
	if p.queue != nil {
		close(p.queue)
		p.writers.Wait()
	}
}

type appendReq struct {
	sql  string
	args []any
	done chan error // buffered
}

func (p *Postgres) appendGrouped(ctx context.Context, sessionID string, evs []game.Event) error {
	sql, args, err := appendStmt(sessionID, evs)
	if err != nil {
		return err
	}
	req := &appendReq{sql: sql, args: args, done: make(chan error, 1)}
	select {
	case p.queue <- req:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-req.done:
		return err
	case <-ctx.Done():
		return ctx.Err() // it may still commit; the actor's next append will tell
	}
}

func (p *Postgres) writer(maxBatch int) {
	for first := range p.queue {
		batch := []*appendReq{first}
	fill:
		for len(batch) < maxBatch {
			select {
			case r, ok := <-p.queue:
				if !ok {
					break fill
				}
				batch = append(batch, r)
			default:
				break fill
			}
		}
		metrics.AppendBatch.Observe(float64(len(batch)))
		p.commit(batch)
	}
}

// commit runs a batch as one pgx batch, which Postgres executes as one
// implicit transaction: one round trip and one flush. Each statement
// reports its own conflict. If a statement errors, the whole batch rolls
// back, and each append is retried on its own.
func (p *Postgres) commit(batch []*appendReq) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if len(batch) == 1 {
		batch[0].done <- p.exec(ctx, batch[0])
		return
	}
	b := &pgx.Batch{}
	for _, r := range batch {
		b.Queue(r.sql, r.args...)
	}
	br := p.pool.SendBatch(ctx, b)
	results := make([]error, len(batch))
	failed := false
	for i := range batch {
		tag, err := br.Exec()
		switch {
		case err != nil:
			failed = true
		case tag.RowsAffected() == 0:
			results[i] = ErrConflict
		}
	}
	closeErr := br.Close()
	if failed {
		for _, r := range batch {
			r.done <- p.exec(ctx, r)
		}
		return
	}
	for i, r := range batch {
		if closeErr != nil {
			results[i] = closeErr // the commit itself failed
		}
		r.done <- results[i]
	}
}

func (p *Postgres) exec(ctx context.Context, r *appendReq) error {
	tag, err := p.pool.Exec(ctx, r.sql, r.args...)
	return appendResult(tag, err)
}

func appendResult(tag pgconn.CommandTag, err error) error {
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr) && pgErr.Code == "23505": // unique_violation on (session_id, seq)
		return ErrConflict
	case err != nil:
		return err
	case tag.RowsAffected() == 0:
		return ErrConflict
	}
	return nil
}

type execer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

// appendStmt builds the statement that appends evs for a session in one
// go: it moves the session's last_seq forward only from the expected value
// and inserts the events only if that matched, so a stale writer (a second
// actor for the session) inserts nothing and affects no rows.
func appendStmt(sessionID string, evs []game.Event) (string, []any, error) {
	n := len(evs)
	seqs, names, bys, causes, datas, ats := make([]int64, n), make([]string, n), make([]string, n), make([]*string, n), make([]string, n), make([]time.Time, n)
	versions := make([]int16, n)
	for i, ev := range evs {
		if ev.Seq != evs[0].Seq+int64(i) {
			return "", nil, fmt.Errorf("seqs not consecutive at %d", ev.Seq)
		}
		data, err := json.Marshal(ev.Data)
		if err != nil {
			return "", nil, err
		}
		seqs[i], names[i], bys[i], datas[i], ats[i] = ev.Seq, ev.Name, string(ev.By), string(data), ev.At
		versions[i] = int16(game.Version(ev.Name)) //nolint:gosec // versions are small
		if ev.Cause != "" {
			causes[i] = &evs[i].Cause
		}
	}
	return `
		WITH moved AS (
			UPDATE sessions SET last_seq = $10 WHERE id = $1 AND last_seq = $9 RETURNING 1
		)
		INSERT INTO events (session_id, seq, name, by_user, cause, data, version, at)
		SELECT $1, seq, name, by_user, cause, data::jsonb, version, at
		FROM unnest($2::bigint[], $3::text[], $4::text[], $5::text[], $6::text[], $7::smallint[], $8::timestamptz[])
			AS e(seq, name, by_user, cause, data, version, at)
		WHERE EXISTS (SELECT 1 FROM moved)`,
		[]any{sessionID, seqs, names, bys, causes, datas, versions, ats, seqs[0] - 1, seqs[n-1]}, nil
}

// appendTx runs appendStmt on its own: one round trip, no explicit
// transaction.
func appendTx(ctx context.Context, db execer, sessionID string, evs []game.Event) error {
	sql, args, err := appendStmt(sessionID, evs)
	if err != nil {
		return err
	}
	tag, err := db.Exec(ctx, sql, args...)
	return appendResult(tag, err)
}
