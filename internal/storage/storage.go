// Package storage defines the persistence models and the Storage interface.
// The SQLite implementation lives in sqlite.go; the interface keeps the door
// open for a Postgres (pgx) backend later.
package storage

import (
	"context"
	"time"
)

// Forum identifiers.
const (
	ForumLolz   = "lolz"
	ForumMipped = "mipped"
)

// Account statuses.
const (
	StatusOK         = "ok"
	StatusAuthFailed = "auth_failed"
	StatusUnknown    = "unknown"
)

// Settings keys.
const (
	KeyDefaultIntervalSec = "default_interval_sec"
	KeyLolzIntervalSec    = "lolz_interval_sec"
	KeyJitterSec          = "jitter_sec"
	KeyStatsPollSec       = "stats_poll_sec"
	KeyRequestDelayMS     = "request_delay_ms"
	KeyNotifications      = "notifications" // "1"/"0"
)

// Account is a forum account (lolz token or mipped credentials).
type Account struct {
	ID         int64
	Forum      string // ForumLolz | ForumMipped
	Label      string
	SecretEnc  []byte // encrypted: lolz token, or mipped JSON {login,password}
	SessionEnc []byte // encrypted mipped cookies (nil for lolz / not-yet-logged-in)
	Proxy      string // socks5://user:pass@ip:port (optional)
	Status     string // StatusOK | StatusAuthFailed | StatusUnknown
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Thread is a tracked forum thread.
type Thread struct {
	ID          int64
	AccountID   int64
	Forum       string
	ThreadRef   string // lolz: numeric thread_id; mipped: full thread URL
	Title       string
	IntervalSec int
	Enabled     bool
	NextBumpAt  *time.Time
	LastBumpAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// BumpLog is one bump attempt record.
type BumpLog struct {
	ID       int64
	ThreadID int64
	At       time.Time
	OK       bool
	Message  string
	NextAt   *time.Time
}

// StatsSnapshot is a point-in-time views/replies sample for a thread.
type StatsSnapshot struct {
	ID       int64
	ThreadID int64
	At       time.Time
	Views    *int
	Replies  *int
}

// Storage is the persistence contract. All methods take a context.
type Storage interface {
	Migrate(ctx context.Context) error
	Close() error

	// Accounts
	CreateAccount(ctx context.Context, a *Account) error
	GetAccount(ctx context.Context, id int64) (*Account, error)
	ListAccounts(ctx context.Context) ([]Account, error)
	UpdateAccount(ctx context.Context, a *Account) error
	SetAccountStatus(ctx context.Context, id int64, status string) error
	SetAccountSession(ctx context.Context, id int64, sessionEnc []byte) error
	SetAccountProxy(ctx context.Context, id int64, proxy string) error
	DeleteAccount(ctx context.Context, id int64) error
	CountThreads(ctx context.Context, accountID int64) (int, error)

	// Threads
	CreateThread(ctx context.Context, t *Thread) error
	GetThread(ctx context.Context, id int64) (*Thread, error)
	ListThreads(ctx context.Context) ([]Thread, error)
	ListThreadsByAccount(ctx context.Context, accountID int64) ([]Thread, error)
	ListEnabledThreads(ctx context.Context) ([]Thread, error)
	ListDueThreads(ctx context.Context, now time.Time) ([]Thread, error)
	UpdateThread(ctx context.Context, t *Thread) error
	DeleteThread(ctx context.Context, id int64) error

	// Bump log
	AddBumpLog(ctx context.Context, l *BumpLog) error
	// ListBumpLogs returns most-recent first. threadID == 0 means all threads.
	ListBumpLogs(ctx context.Context, threadID int64, limit int) ([]BumpLog, error)
	CountSuccessfulBumps(ctx context.Context, threadID int64) (int, error)

	// Stats snapshots
	AddStatsSnapshot(ctx context.Context, s *StatsSnapshot) error
	LatestSnapshot(ctx context.Context, threadID int64) (*StatsSnapshot, error)
	// SnapshotAround returns the snapshot nearest to (and at/after) t, used for deltas.
	SnapshotAround(ctx context.Context, threadID int64, t time.Time) (*StatsSnapshot, error)
	ListSnapshots(ctx context.Context, threadID int64, since time.Time, limit int) ([]StatsSnapshot, error)

	// Settings
	GetSetting(ctx context.Context, key string) (string, bool, error)
	SetSetting(ctx context.Context, key, value string) error
	AllSettings(ctx context.Context) (map[string]string, error)
}

// ErrNotFound is returned when a requested row does not exist.
type notFound struct{ what string }

func (e notFound) Error() string { return e.what + " not found" }

// IsNotFound reports whether err signals a missing row.
func IsNotFound(err error) bool {
	_, ok := err.(notFound)
	return ok
}
