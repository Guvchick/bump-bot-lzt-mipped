package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/guvchick/bumpbot/migrations"
	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
)

// SQLite is the modernc.org/sqlite-backed Storage implementation.
type SQLite struct {
	db *sql.DB
}

var _ Storage = (*SQLite)(nil)

// OpenSQLite opens (and creates if needed) the SQLite database at path.
func OpenSQLite(path string) (*SQLite, error) {
	// _pragma options: WAL for concurrent reads, foreign_keys for ON DELETE CASCADE,
	// busy_timeout so the scheduler and the TG panel don't trip over each other.
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite writers are serialized; a single connection avoids "database is locked".
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return &SQLite{db: db}, nil
}

// Migrate applies every embedded *.sql migration in lexical order.
func (s *SQLite) Migrate(ctx context.Context) error {
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		b, err := migrations.FS.ReadFile(name)
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, string(b)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
	}
	return nil
}

// Close closes the underlying DB.
func (s *SQLite) Close() error { return s.db.Close() }

// ---- Accounts ----

func (s *SQLite) CreateAccount(ctx context.Context, a *Account) error {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO accounts (forum, label, secret_enc, session_enc, proxy, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		a.Forum, a.Label, a.SecretEnc, nullBytes(a.SessionEnc), a.Proxy, defStr(a.Status, StatusUnknown), now, now)
	if err != nil {
		return err
	}
	a.ID, err = res.LastInsertId()
	a.CreatedAt, a.UpdatedAt = now, now
	return err
}

func (s *SQLite) GetAccount(ctx context.Context, id int64) (*Account, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, forum, label, secret_enc, session_enc, proxy, status, created_at, updated_at
		 FROM accounts WHERE id = ?`, id)
	return scanAccount(row)
}

func (s *SQLite) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, forum, label, secret_enc, session_enc, proxy, status, created_at, updated_at
		 FROM accounts ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (s *SQLite) UpdateAccount(ctx context.Context, a *Account) error {
	a.UpdatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`UPDATE accounts SET forum=?, label=?, secret_enc=?, session_enc=?, proxy=?, status=?, updated_at=?
		 WHERE id=?`,
		a.Forum, a.Label, a.SecretEnc, nullBytes(a.SessionEnc), a.Proxy, a.Status, a.UpdatedAt, a.ID)
	return err
}

func (s *SQLite) SetAccountStatus(ctx context.Context, id int64, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE accounts SET status=?, updated_at=? WHERE id=?`, status, time.Now().UTC(), id)
	return err
}

func (s *SQLite) SetAccountSession(ctx context.Context, id int64, sessionEnc []byte) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE accounts SET session_enc=?, updated_at=? WHERE id=?`, nullBytes(sessionEnc), time.Now().UTC(), id)
	return err
}

func (s *SQLite) SetAccountProxy(ctx context.Context, id int64, proxy string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE accounts SET proxy=?, updated_at=? WHERE id=?`, proxy, time.Now().UTC(), id)
	return err
}

func (s *SQLite) DeleteAccount(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM accounts WHERE id=?`, id)
	return err
}

func (s *SQLite) CountThreads(ctx context.Context, accountID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM threads WHERE account_id=?`, accountID).Scan(&n)
	return n, err
}

// ---- Threads ----

func (s *SQLite) CreateThread(ctx context.Context, t *Thread) error {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO threads (account_id, forum, thread_ref, title, interval_sec, enabled, next_bump_at, last_bump_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.AccountID, t.Forum, t.ThreadRef, t.Title, t.IntervalSec, boolToInt(t.Enabled),
		nullTime(t.NextBumpAt), nullTime(t.LastBumpAt), now, now)
	if err != nil {
		return err
	}
	t.ID, err = res.LastInsertId()
	t.CreatedAt, t.UpdatedAt = now, now
	return err
}

func (s *SQLite) GetThread(ctx context.Context, id int64) (*Thread, error) {
	row := s.db.QueryRowContext(ctx, threadCols+` WHERE id=?`, id)
	return scanThread(row)
}

func (s *SQLite) ListThreads(ctx context.Context) ([]Thread, error) {
	return s.queryThreads(ctx, threadCols+` ORDER BY id`)
}

func (s *SQLite) ListThreadsByAccount(ctx context.Context, accountID int64) ([]Thread, error) {
	return s.queryThreads(ctx, threadCols+` WHERE account_id=? ORDER BY id`, accountID)
}

func (s *SQLite) ListEnabledThreads(ctx context.Context) ([]Thread, error) {
	return s.queryThreads(ctx, threadCols+` WHERE enabled=1 ORDER BY id`)
}

func (s *SQLite) ListDueThreads(ctx context.Context, now time.Time) ([]Thread, error) {
	// A NULL next_bump_at means "never scheduled" -> due immediately.
	return s.queryThreads(ctx,
		threadCols+` WHERE enabled=1 AND (next_bump_at IS NULL OR next_bump_at <= ?) ORDER BY next_bump_at`,
		now.UTC())
}

func (s *SQLite) UpdateThread(ctx context.Context, t *Thread) error {
	t.UpdatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`UPDATE threads SET account_id=?, forum=?, thread_ref=?, title=?, interval_sec=?, enabled=?,
		 next_bump_at=?, last_bump_at=?, updated_at=? WHERE id=?`,
		t.AccountID, t.Forum, t.ThreadRef, t.Title, t.IntervalSec, boolToInt(t.Enabled),
		nullTime(t.NextBumpAt), nullTime(t.LastBumpAt), t.UpdatedAt, t.ID)
	return err
}

func (s *SQLite) DeleteThread(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM threads WHERE id=?`, id)
	return err
}

const threadCols = `SELECT id, account_id, forum, thread_ref, title, interval_sec, enabled, next_bump_at, last_bump_at, created_at, updated_at FROM threads`

func (s *SQLite) queryThreads(ctx context.Context, query string, args ...any) ([]Thread, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Thread
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// ---- Bump log ----

func (s *SQLite) AddBumpLog(ctx context.Context, l *BumpLog) error {
	if l.At.IsZero() {
		l.At = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO bump_log (thread_id, at, ok, message, next_at) VALUES (?, ?, ?, ?, ?)`,
		l.ThreadID, l.At, boolToInt(l.OK), l.Message, nullTime(l.NextAt))
	if err != nil {
		return err
	}
	l.ID, err = res.LastInsertId()
	return err
}

func (s *SQLite) ListBumpLogs(ctx context.Context, threadID int64, limit int) ([]BumpLog, error) {
	if limit <= 0 {
		limit = 20
	}
	var (
		rows *sql.Rows
		err  error
	)
	if threadID == 0 {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, thread_id, at, ok, message, next_at FROM bump_log ORDER BY at DESC, id DESC LIMIT ?`, limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, thread_id, at, ok, message, next_at FROM bump_log WHERE thread_id=? ORDER BY at DESC, id DESC LIMIT ?`,
			threadID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BumpLog
	for rows.Next() {
		var (
			l      BumpLog
			okInt  int
			nextAt sql.NullTime
		)
		if err := rows.Scan(&l.ID, &l.ThreadID, &l.At, &okInt, &l.Message, &nextAt); err != nil {
			return nil, err
		}
		l.OK = okInt != 0
		if nextAt.Valid {
			t := nextAt.Time
			l.NextAt = &t
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *SQLite) CountSuccessfulBumps(ctx context.Context, threadID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bump_log WHERE thread_id=? AND ok=1`, threadID).Scan(&n)
	return n, err
}

// ---- Stats snapshots ----

func (s *SQLite) AddStatsSnapshot(ctx context.Context, sn *StatsSnapshot) error {
	if sn.At.IsZero() {
		sn.At = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO stats_snapshots (thread_id, at, views, replies) VALUES (?, ?, ?, ?)`,
		sn.ThreadID, sn.At, nullInt(sn.Views), nullInt(sn.Replies))
	if err != nil {
		return err
	}
	sn.ID, err = res.LastInsertId()
	return err
}

func (s *SQLite) LatestSnapshot(ctx context.Context, threadID int64) (*StatsSnapshot, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, thread_id, at, views, replies FROM stats_snapshots WHERE thread_id=? ORDER BY at DESC LIMIT 1`, threadID)
	return scanSnapshot(row)
}

func (s *SQLite) SnapshotAround(ctx context.Context, threadID int64, t time.Time) (*StatsSnapshot, error) {
	// Earliest snapshot at/after t; if none, fall back to the oldest available.
	row := s.db.QueryRowContext(ctx,
		`SELECT id, thread_id, at, views, replies FROM stats_snapshots
		 WHERE thread_id=? AND at >= ? ORDER BY at ASC LIMIT 1`, threadID, t.UTC())
	sn, err := scanSnapshot(row)
	if IsNotFound(err) {
		row = s.db.QueryRowContext(ctx,
			`SELECT id, thread_id, at, views, replies FROM stats_snapshots
			 WHERE thread_id=? ORDER BY at ASC LIMIT 1`, threadID)
		return scanSnapshot(row)
	}
	return sn, err
}

func (s *SQLite) ListSnapshots(ctx context.Context, threadID int64, since time.Time, limit int) ([]StatsSnapshot, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, thread_id, at, views, replies FROM stats_snapshots
		 WHERE thread_id=? AND at >= ? ORDER BY at ASC LIMIT ?`, threadID, since.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StatsSnapshot
	for rows.Next() {
		sn, err := scanSnapshotRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sn)
	}
	return out, rows.Err()
}

// ---- Settings ----

func (s *SQLite) GetSetting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (s *SQLite) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (s *SQLite) AllSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// ---- scan helpers ----

type scanner interface {
	Scan(dest ...any) error
}

func scanAccount(r scanner) (*Account, error) {
	var (
		a       Account
		session []byte
	)
	err := r.Scan(&a.ID, &a.Forum, &a.Label, &a.SecretEnc, &session, &a.Proxy, &a.Status, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound{"account"}
	}
	if err != nil {
		return nil, err
	}
	a.SessionEnc = session
	return &a, nil
}

func scanThread(r scanner) (*Thread, error) {
	var (
		t        Thread
		enabled  int
		nextBump sql.NullTime
		lastBump sql.NullTime
	)
	err := r.Scan(&t.ID, &t.AccountID, &t.Forum, &t.ThreadRef, &t.Title, &t.IntervalSec, &enabled,
		&nextBump, &lastBump, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound{"thread"}
	}
	if err != nil {
		return nil, err
	}
	t.Enabled = enabled != 0
	if nextBump.Valid {
		v := nextBump.Time
		t.NextBumpAt = &v
	}
	if lastBump.Valid {
		v := lastBump.Time
		t.LastBumpAt = &v
	}
	return &t, nil
}

func scanSnapshot(r scanner) (*StatsSnapshot, error) {
	var (
		sn      StatsSnapshot
		views   sql.NullInt64
		replies sql.NullInt64
	)
	err := r.Scan(&sn.ID, &sn.ThreadID, &sn.At, &views, &replies)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound{"snapshot"}
	}
	if err != nil {
		return nil, err
	}
	if views.Valid {
		v := int(views.Int64)
		sn.Views = &v
	}
	if replies.Valid {
		v := int(replies.Int64)
		sn.Replies = &v
	}
	return &sn, nil
}

func scanSnapshotRows(r scanner) (*StatsSnapshot, error) { return scanSnapshot(r) }

// ---- null helpers ----

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}

func nullInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func defStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
