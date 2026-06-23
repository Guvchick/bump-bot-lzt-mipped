-- Schema for the bump bot. Applied automatically at startup (idempotent).

CREATE TABLE IF NOT EXISTS accounts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    forum       TEXT    NOT NULL,
    label       TEXT    NOT NULL,
    secret_enc  BLOB    NOT NULL,
    session_enc BLOB,
    proxy       TEXT    NOT NULL DEFAULT '',
    status      TEXT    NOT NULL DEFAULT 'unknown',
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS threads (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id   INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    forum        TEXT    NOT NULL,
    thread_ref   TEXT    NOT NULL,
    title        TEXT    NOT NULL DEFAULT '',
    interval_sec INTEGER NOT NULL,
    enabled      INTEGER NOT NULL DEFAULT 1,
    next_bump_at TIMESTAMP,
    last_bump_at TIMESTAMP,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_threads_account ON threads(account_id);
CREATE INDEX IF NOT EXISTS idx_threads_due ON threads(enabled, next_bump_at);

CREATE TABLE IF NOT EXISTS bump_log (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    thread_id INTEGER NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ok        INTEGER NOT NULL,
    message   TEXT    NOT NULL DEFAULT '',
    next_at   TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_bumplog_thread ON bump_log(thread_id, at DESC);

CREATE TABLE IF NOT EXISTS stats_snapshots (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    thread_id INTEGER NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    views     INTEGER,
    replies   INTEGER
);

CREATE INDEX IF NOT EXISTS idx_snap_thread ON stats_snapshots(thread_id, at DESC);

CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
