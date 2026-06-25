// Package forum abstracts the two supported forums behind a single Forum
// interface. lolz.go talks to the official Lolzteam REST API; mipped.go drives
// the Mipped (XenForo) site via a logged-in session.
package forum

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ErrAuthFailed signals an invalid/expired token or session. The scheduler uses
// it to flag the account as auth_failed.
var ErrAuthFailed = errors.New("authentication failed")

// Account is the decrypted view of a stored account passed to a Forum client.
type Account struct {
	ID    int64
	Forum string
	Label string

	// Secret is the decrypted secret:
	//   lolz   -> the raw API token string (as bytes)
	//   mipped -> JSON {"login":"...","password":"..."}
	Secret []byte

	// Session holds decrypted Mipped cookies (nil for lolz / before first login).
	Session []byte

	// Proxy is an optional socks5://user:pass@host:port URL.
	Proxy string

	// SaveSession persists refreshed Mipped cookies. The mipped client calls it
	// after a successful login so the session is reused next time. May be nil.
	SaveSession func(ctx context.Context, session []byte) error
}

// Thread is the minimal thread reference a Forum client needs.
type Thread struct {
	ID  int64
	Ref string // lolz: numeric id; mipped: full thread URL
}

// BumpResult is the outcome of a Bump attempt.
type BumpResult struct {
	OK         bool          // true if the thread was actually bumped
	Message    string        // forum response text (for the log)
	RetryAfter time.Duration // if > 0, the forum asked us to wait this long
}

// ThreadStats is best-effort thread statistics. Pointers are nil when unknown.
type ThreadStats struct {
	Title   string
	Views   *int
	Replies *int
}

// Forum is the contract implemented by lolz and mipped clients.
type Forum interface {
	// Bump raises the thread. A non-nil error means a transport/auth failure;
	// a "too early" response is reported via BumpResult (OK=false, RetryAfter>0),
	// not as an error.
	Bump(ctx context.Context, acc Account, t Thread) (BumpResult, error)
	// ThreadStats returns views/replies/title (best effort).
	ThreadStats(ctx context.Context, acc Account, t Thread) (ThreadStats, error)
	// CheckAuth verifies the token/session is alive.
	CheckAuth(ctx context.Context, acc Account) error
}

// DiscoveredThread is a thread found by auto-import.
type DiscoveredThread struct {
	Ref   string // stored as-is into threads.thread_ref (lolz: id; mipped: full URL)
	Title string
}

// ListOptions tunes auto-import.
type ListOptions struct {
	// MaxAge skips threads created longer ago than this. Zero means no age limit.
	// Only forums that expose a create date (lolz) honour it.
	MaxAge time.Duration
}

// ThreadLister is implemented by forums that can enumerate the authenticated
// account's own threads, so the bot can import them automatically.
type ThreadLister interface {
	MyThreads(ctx context.Context, acc Account, opts ListOptions) ([]DiscoveredThread, error)
}

// CanonicalRef returns a stable key for de-duplicating thread references that
// may be written in different forms (e.g. a Mipped URL with/without trailing
// slash, or just slug.id).
func CanonicalRef(forumName, ref string) string {
	if forumName == "mipped" {
		if _, id, err := parseMippedRef(ref); err == nil {
			return "m:" + id
		}
	}
	return strings.TrimSpace(ref)
}
