// Package store persists sessions, members, and each session's append-only
// event log in Postgres.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SpandanDhru/yorm/internal/game"
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

// Load returns every event of the session in seq order, or ErrNotFound if
// the session does not exist.
func (p *Postgres) Load(ctx context.Context, sessionID string) ([]game.Event, error) {
	var exists bool
	if err := p.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sessions WHERE id = $1)`, sessionID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("store: load: %w", err)
	}
	if !exists {
		return nil, ErrNotFound
	}
	rows, err := p.pool.Query(ctx,
		`SELECT seq, name, by_user, coalesce(cause, ''), data, at FROM events WHERE session_id = $1 ORDER BY seq`,
		sessionID)
	if err != nil {
		return nil, fmt.Errorf("store: load: %w", err)
	}
	defer rows.Close()
	var evs []game.Event
	for rows.Next() {
		var ev game.Event
		var data []byte
		if err := rows.Scan(&ev.Seq, &ev.Name, &ev.By, &ev.Cause, &data, &ev.At); err != nil {
			return nil, fmt.Errorf("store: load: %w", err)
		}
		if ev.Data, err = game.DecodePayload(ev.Name, data); err != nil {
			return nil, fmt.Errorf("store: load seq %d: %w", ev.Seq, err)
		}
		evs = append(evs, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: load: %w", err)
	}
	return evs, nil
}

// Append stores evs, which must have consecutive seqs following the
// session's last one, in a single insert. If another writer got there first
// it returns ErrConflict and stores nothing.
func (p *Postgres) Append(ctx context.Context, sessionID string, evs []game.Event) error {
	if len(evs) == 0 {
		return nil
	}
	err := pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		return appendTx(ctx, tx, sessionID, evs)
	})
	if err != nil && !errors.Is(err, ErrConflict) {
		return fmt.Errorf("store: append: %w", err)
	}
	return err
}

func appendTx(ctx context.Context, tx pgx.Tx, sessionID string, evs []game.Event) error {
	n := len(evs)
	seqs, names, bys, causes, datas, ats := make([]int64, n), make([]string, n), make([]string, n), make([]*string, n), make([]string, n), make([]time.Time, n)
	for i, ev := range evs {
		if ev.Seq != evs[0].Seq+int64(i) {
			return fmt.Errorf("seqs not consecutive at %d", ev.Seq)
		}
		data, err := json.Marshal(ev.Data)
		if err != nil {
			return err
		}
		seqs[i], names[i], bys[i], datas[i], ats[i] = ev.Seq, ev.Name, string(ev.By), string(data), ev.At
		if ev.Cause != "" {
			causes[i] = &evs[i].Cause
		}
	}

	// Moving last_seq forward only from the expected value serializes
	// writers: a second actor for the session matches no row.
	tag, err := tx.Exec(ctx,
		`UPDATE sessions SET last_seq = $3 WHERE id = $1 AND last_seq = $2`,
		sessionID, seqs[0]-1, seqs[n-1])
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO events (session_id, seq, name, by_user, cause, data, at)
		SELECT $1, seq, name, by_user, cause, data::jsonb, at
		FROM unnest($2::bigint[], $3::text[], $4::text[], $5::text[], $6::text[], $7::timestamptz[])
			AS e(seq, name, by_user, cause, data, at)`,
		sessionID, seqs, names, bys, causes, datas, ats)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation on (session_id, seq)
		return ErrConflict
	}
	return err
}
