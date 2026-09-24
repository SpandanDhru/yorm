-- +goose Up
CREATE TABLE sessions (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  invite_code  TEXT NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seq     BIGINT NOT NULL DEFAULT 0
);

-- Append-only. The primary key also stops two actors for the same session
-- from both writing: the second one gets a unique violation and shuts down.
CREATE TABLE events (
  session_id  TEXT     NOT NULL REFERENCES sessions(id),
  seq         BIGINT   NOT NULL,
  name        TEXT     NOT NULL,
  by_user     TEXT     NOT NULL,
  cause       TEXT,                  -- command ID
  data        JSONB    NOT NULL,
  version     SMALLINT NOT NULL DEFAULT 1,
  at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (session_id, seq)
);

CREATE TABLE snapshots (
  session_id  TEXT   NOT NULL REFERENCES sessions(id),
  seq         BIGINT NOT NULL,
  state       JSONB  NOT NULL,
  at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (session_id, seq)
);

CREATE TABLE members (
  session_id    TEXT NOT NULL REFERENCES sessions(id),
  user_id       TEXT NOT NULL,
  display_name  TEXT NOT NULL,
  role          TEXT NOT NULL,
  PRIMARY KEY (session_id, user_id)
);

-- +goose Down
DROP TABLE members;
DROP TABLE snapshots;
DROP TABLE events;
DROP TABLE sessions;
