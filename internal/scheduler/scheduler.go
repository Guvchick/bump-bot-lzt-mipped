// Package scheduler runs the bump loop and the stats-polling loop. It owns the
// global request pacing (delay_min), per-thread backoff, and jitter, and exposes
// helpers the Telegram panel calls directly (CheckAuth, FetchStats, BumpNow).
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strconv"
	"sync"
	"time"

	"github.com/guvchick/bumpbot/internal/config"
	"github.com/guvchick/bumpbot/internal/crypto"
	"github.com/guvchick/bumpbot/internal/forum"
	"github.com/guvchick/bumpbot/internal/storage"
)

const (
	tickInterval   = 30 * time.Second // how often we scan for due threads
	retryBuffer    = 60 * time.Second // added to a forum-provided RetryAfter
	authRetryDelay = 30 * time.Minute // wait before retrying an auth-failed thread
	failNotifyN    = 3                // notify after this many consecutive failures
)

// backoffSteps is the exponential backoff ladder for network/unknown errors.
var backoffSteps = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour}

// Settings is the live, editable subset of configuration.
type Settings struct {
	DefaultIntervalSec int
	LolzIntervalSec    int
	JitterSec          int
	StatsPollSec       int
	RequestDelayMS     int
	Notifications      bool
}

// Scheduler coordinates bumping and stats collection.
type Scheduler struct {
	store  storage.Storage
	crypto *crypto.Crypto
	forums map[string]forum.Forum
	cfg    *config.Config
	log    *slog.Logger

	setMu sync.RWMutex
	set   Settings

	paceMu  sync.Mutex
	lastReq time.Time

	stateMu  sync.Mutex
	backoff  map[int64]int // thread id -> index into backoffSteps
	failures map[int64]int // thread id -> consecutive failures

	notifyMu sync.RWMutex
	notify   func(string)
}

// New builds a Scheduler. forums maps storage.Forum* -> implementation.
func New(store storage.Storage, cr *crypto.Crypto, forums map[string]forum.Forum, cfg *config.Config, log *slog.Logger) *Scheduler {
	return &Scheduler{
		store:    store,
		crypto:   cr,
		forums:   forums,
		cfg:      cfg,
		log:      log,
		backoff:  make(map[int64]int),
		failures: make(map[int64]int),
		set: Settings{
			DefaultIntervalSec: cfg.DefaultIntervalSec,
			LolzIntervalSec:    cfg.LolzIntervalSec,
			JitterSec:          cfg.JitterSec,
			StatsPollSec:       cfg.StatsPollSec,
			RequestDelayMS:     cfg.RequestDelayMS,
			Notifications:      true,
		},
	}
}

// SetNotifier registers a callback used to push notifications to the owner chat.
func (s *Scheduler) SetNotifier(fn func(string)) {
	s.notifyMu.Lock()
	s.notify = fn
	s.notifyMu.Unlock()
}

// CurrentSettings returns a snapshot of the live settings.
func (s *Scheduler) CurrentSettings() Settings {
	s.setMu.RLock()
	defer s.setMu.RUnlock()
	return s.set
}

// Reload loads the editable settings from storage (falling back to .env defaults).
func (s *Scheduler) Reload(ctx context.Context) error {
	all, err := s.store.AllSettings(ctx)
	if err != nil {
		return err
	}
	get := func(key string, def int) int {
		if v, ok := all[key]; ok {
			if n, err := strconv.Atoi(v); err == nil {
				return n
			}
		}
		return def
	}
	set := Settings{
		DefaultIntervalSec: get(storage.KeyDefaultIntervalSec, s.cfg.DefaultIntervalSec),
		LolzIntervalSec:    get(storage.KeyLolzIntervalSec, s.cfg.LolzIntervalSec),
		JitterSec:          get(storage.KeyJitterSec, s.cfg.JitterSec),
		StatsPollSec:       get(storage.KeyStatsPollSec, s.cfg.StatsPollSec),
		RequestDelayMS:     get(storage.KeyRequestDelayMS, s.cfg.RequestDelayMS),
		Notifications:      all[storage.KeyNotifications] != "0",
	}
	s.setMu.Lock()
	s.set = set
	s.setMu.Unlock()
	return nil
}

// SeedSettings writes the .env defaults into the settings table if they are
// missing, so the panel can show and edit them.
func (s *Scheduler) SeedSettings(ctx context.Context) error {
	defaults := map[string]string{
		storage.KeyDefaultIntervalSec: strconv.Itoa(s.cfg.DefaultIntervalSec),
		storage.KeyLolzIntervalSec:    strconv.Itoa(s.cfg.LolzIntervalSec),
		storage.KeyJitterSec:          strconv.Itoa(s.cfg.JitterSec),
		storage.KeyStatsPollSec:       strconv.Itoa(s.cfg.StatsPollSec),
		storage.KeyRequestDelayMS:     strconv.Itoa(s.cfg.RequestDelayMS),
		storage.KeyNotifications:      "1",
	}
	for k, v := range defaults {
		if _, ok, err := s.store.GetSetting(ctx, k); err != nil {
			return err
		} else if !ok {
			if err := s.store.SetSetting(ctx, k, v); err != nil {
				return err
			}
		}
	}
	return s.Reload(ctx)
}

// DefaultInterval returns the default interval (seconds) for a forum.
func (s *Scheduler) DefaultInterval(forumName string) int {
	st := s.CurrentSettings()
	if forumName == storage.ForumLolz {
		return st.LolzIntervalSec
	}
	return st.DefaultIntervalSec
}

// Run starts the bump and stats loops; both stop when ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	go s.bumpLoop(ctx)
	go s.statsLoop(ctx)
}

func (s *Scheduler) bumpLoop(ctx context.Context) {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	s.processDue(ctx) // catch up on anything already due at startup
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.processDue(ctx)
		}
	}
}

func (s *Scheduler) processDue(ctx context.Context) {
	due, err := s.store.ListDueThreads(ctx, time.Now())
	if err != nil {
		s.log.Error("list due threads", "err", err)
		return
	}
	for _, t := range due {
		if ctx.Err() != nil {
			return
		}
		if _, err := s.bumpThread(ctx, t); err != nil {
			s.log.Warn("bump", "thread", t.ID, "err", err)
		}
	}
}

// bumpThread performs one bump and persists the outcome. It is used by both the
// loop and the manual "bump now" panel action; it returns the forum result so
// the panel can show it.
func (s *Scheduler) bumpThread(ctx context.Context, t storage.Thread) (forum.BumpResult, error) {
	acc, err := s.store.GetAccount(ctx, t.AccountID)
	if err != nil {
		return forum.BumpResult{}, fmt.Errorf("load account: %w", err)
	}
	f := s.forums[t.Forum]
	if f == nil {
		return forum.BumpResult{}, fmt.Errorf("unknown forum %q", t.Forum)
	}
	fa, err := s.forumAccount(acc)
	if err != nil {
		return forum.BumpResult{}, err
	}

	s.pace(ctx)
	res, bumpErr := f.Bump(ctx, fa, forum.Thread{ID: t.ID, Ref: t.ThreadRef})
	now := time.Now().UTC()

	var (
		logOK   bool
		logMsg  string
		nextAt  time.Time
		hasNext bool
	)
	switch {
	case errors.Is(bumpErr, forum.ErrAuthFailed):
		_ = s.store.SetAccountStatus(ctx, acc.ID, storage.StatusAuthFailed)
		logMsg = "auth failed"
		nextAt, hasNext = now.Add(authRetryDelay), true
		s.notifyf("🔴 Аккаунт «%s»: ошибка авторизации", acc.Label)

	case bumpErr != nil:
		d := s.nextBackoff(t.ID)
		logMsg = "error: " + bumpErr.Error()
		nextAt, hasNext = now.Add(d), true

	default: // no transport/auth error
		if acc.Status != storage.StatusOK {
			_ = s.store.SetAccountStatus(ctx, acc.ID, storage.StatusOK)
		}
		switch {
		case res.OK:
			s.resetBackoff(t.ID)
			t.LastBumpAt = &now
			interval := time.Duration(t.IntervalSec)*time.Second + s.jitter()
			logOK, logMsg = true, res.Message
			nextAt, hasNext = now.Add(interval), true
		case res.RetryAfter > 0:
			s.resetBackoff(t.ID)
			logMsg = res.Message
			nextAt, hasNext = now.Add(res.RetryAfter+retryBuffer), true
		default:
			// Soft rejection with no time hint — try again next interval.
			s.resetBackoff(t.ID)
			interval := time.Duration(t.IntervalSec)*time.Second + s.jitter()
			logMsg = res.Message
			nextAt, hasNext = now.Add(interval), true
		}
	}

	if hasNext {
		t.NextBumpAt = &nextAt
	}
	if err := s.store.UpdateThread(ctx, &t); err != nil {
		s.log.Error("update thread", "thread", t.ID, "err", err)
	}
	logEntry := &storage.BumpLog{ThreadID: t.ID, At: now, OK: logOK, Message: logMsg}
	if hasNext {
		logEntry.NextAt = &nextAt
	}
	if err := s.store.AddBumpLog(ctx, logEntry); err != nil {
		s.log.Error("add bump log", "thread", t.ID, "err", err)
	}

	s.trackFailure(ctx, &t, logOK)
	s.log.Info("bump", "thread", t.ID, "forum", t.Forum, "ok", logOK, "msg", logMsg)
	return res, bumpErr
}

// BumpNow runs an out-of-schedule bump for a single thread.
func (s *Scheduler) BumpNow(ctx context.Context, threadID int64) (forum.BumpResult, error) {
	t, err := s.store.GetThread(ctx, threadID)
	if err != nil {
		return forum.BumpResult{}, err
	}
	return s.bumpThread(ctx, *t)
}

// CheckAuth verifies an account and records its status.
func (s *Scheduler) CheckAuth(ctx context.Context, a *storage.Account) error {
	f := s.forums[a.Forum]
	if f == nil {
		return fmt.Errorf("unknown forum %q", a.Forum)
	}
	fa, err := s.forumAccount(a)
	if err != nil {
		return err
	}
	s.pace(ctx)
	err = f.CheckAuth(ctx, fa)
	switch {
	case err == nil:
		_ = s.store.SetAccountStatus(ctx, a.ID, storage.StatusOK)
	case errors.Is(err, forum.ErrAuthFailed):
		_ = s.store.SetAccountStatus(ctx, a.ID, storage.StatusAuthFailed)
	}
	return err
}

// ImportThreads discovers the account's own threads and inserts the new ones,
// disabled by default (the user opts each into auto-bump via the panel).
// Returns added (newly inserted) and found (total discovered).
func (s *Scheduler) ImportThreads(ctx context.Context, a *storage.Account) (added, found int, err error) {
	lister, ok := s.forums[a.Forum].(forum.ThreadLister)
	if !ok {
		return 0, 0, fmt.Errorf("импорт не поддерживается для форума %q", a.Forum)
	}
	fa, err := s.forumAccount(a)
	if err != nil {
		return 0, 0, err
	}
	s.pace(ctx)
	discovered, err := lister.MyThreads(ctx, fa)
	if err != nil {
		// Don't flip account status here: a Lolz list 403 usually just means the
		// token lacks the "read" scope even though CheckAuth (basic) passed.
		return 0, 0, err
	}
	found = len(discovered)

	existing, err := s.store.ListThreadsByAccount(ctx, a.ID)
	if err != nil {
		return 0, found, err
	}
	have := make(map[string]bool, len(existing))
	for _, t := range existing {
		have[forum.CanonicalRef(a.Forum, t.ThreadRef)] = true
	}

	interval := s.DefaultInterval(a.Forum)
	for _, d := range discovered {
		key := forum.CanonicalRef(a.Forum, d.Ref)
		if have[key] {
			continue
		}
		have[key] = true
		t := &storage.Thread{
			AccountID:   a.ID,
			Forum:       a.Forum,
			ThreadRef:   d.Ref,
			Title:       d.Title,
			IntervalSec: interval,
			Enabled:     false, // opt-in via panel
		}
		if cerr := s.store.CreateThread(ctx, t); cerr != nil {
			s.log.Error("import: create thread", "account", a.ID, "err", cerr)
			continue
		}
		added++
	}
	return added, found, nil
}

// FetchStats returns live thread stats (used when adding a thread / viewing it).
func (s *Scheduler) FetchStats(ctx context.Context, a *storage.Account, t *storage.Thread) (forum.ThreadStats, error) {
	f := s.forums[a.Forum]
	if f == nil {
		return forum.ThreadStats{}, fmt.Errorf("unknown forum %q", a.Forum)
	}
	fa, err := s.forumAccount(a)
	if err != nil {
		return forum.ThreadStats{}, err
	}
	s.pace(ctx)
	return f.ThreadStats(ctx, fa, forum.Thread{ID: t.ID, Ref: t.ThreadRef})
}

// ---- stats loop ----

func (s *Scheduler) statsLoop(ctx context.Context) {
	for {
		interval := s.statsPollInterval()
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.pollStats(ctx)
		}
	}
}

func (s *Scheduler) pollStats(ctx context.Context) {
	threads, err := s.store.ListEnabledThreads(ctx)
	if err != nil {
		s.log.Error("stats: list threads", "err", err)
		return
	}
	for _, t := range threads {
		if ctx.Err() != nil {
			return
		}
		acc, err := s.store.GetAccount(ctx, t.AccountID)
		if err != nil {
			continue
		}
		st, err := s.FetchStats(ctx, acc, &t)
		if err != nil {
			s.log.Debug("stats: fetch", "thread", t.ID, "err", err)
			continue
		}
		if st.Title != "" && st.Title != t.Title {
			t.Title = st.Title
			_ = s.store.UpdateThread(ctx, &t)
		}
		_ = s.store.AddStatsSnapshot(ctx, &storage.StatsSnapshot{
			ThreadID: t.ID, At: time.Now().UTC(), Views: st.Views, Replies: st.Replies,
		})
	}
}

// ---- helpers ----

// forumAccount decrypts a stored account into the forum-facing view, wiring up a
// SaveSession callback that re-encrypts and persists refreshed Mipped cookies.
func (s *Scheduler) forumAccount(a *storage.Account) (forum.Account, error) {
	secret, err := s.crypto.Decrypt(a.SecretEnc)
	if err != nil {
		return forum.Account{}, fmt.Errorf("decrypt secret: %w", err)
	}
	var session []byte
	if len(a.SessionEnc) > 0 {
		if sess, derr := s.crypto.Decrypt(a.SessionEnc); derr == nil {
			session = sess
		}
	}
	id := a.ID
	return forum.Account{
		ID:      a.ID,
		Forum:   a.Forum,
		Label:   a.Label,
		Secret:  secret,
		Session: session,
		Proxy:   a.Proxy,
		SaveSession: func(ctx context.Context, sess []byte) error {
			enc, err := s.crypto.Encrypt(sess)
			if err != nil {
				return err
			}
			return s.store.SetAccountSession(ctx, id, enc)
		},
	}, nil
}

// pace enforces the global minimum delay between consecutive forum requests.
func (s *Scheduler) pace(ctx context.Context) {
	s.paceMu.Lock()
	defer s.paceMu.Unlock()
	delay := s.requestDelay()
	if wait := time.Until(s.lastReq.Add(delay)); wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
		}
	}
	s.lastReq = time.Now()
}

func (s *Scheduler) jitter() time.Duration {
	st := s.CurrentSettings()
	if st.JitterSec <= 0 {
		return 0
	}
	return time.Duration(rand.Intn(st.JitterSec)) * time.Second
}

func (s *Scheduler) requestDelay() time.Duration {
	st := s.CurrentSettings()
	if st.RequestDelayMS <= 0 {
		return 0
	}
	return time.Duration(st.RequestDelayMS) * time.Millisecond
}

func (s *Scheduler) statsPollInterval() time.Duration {
	st := s.CurrentSettings()
	if st.StatsPollSec <= 0 {
		return time.Hour
	}
	return time.Duration(st.StatsPollSec) * time.Second
}

func (s *Scheduler) nextBackoff(threadID int64) time.Duration {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	idx := s.backoff[threadID]
	if idx >= len(backoffSteps) {
		idx = len(backoffSteps) - 1
	}
	d := backoffSteps[idx]
	s.backoff[threadID] = idx + 1
	return d
}

func (s *Scheduler) resetBackoff(threadID int64) {
	s.stateMu.Lock()
	delete(s.backoff, threadID)
	s.stateMu.Unlock()
}

func (s *Scheduler) trackFailure(ctx context.Context, t *storage.Thread, ok bool) {
	s.stateMu.Lock()
	if ok {
		delete(s.failures, t.ID)
		s.stateMu.Unlock()
		return
	}
	s.failures[t.ID]++
	n := s.failures[t.ID]
	s.stateMu.Unlock()
	if n == failNotifyN {
		name := t.Title
		if name == "" {
			name = t.ThreadRef
		}
		s.notifyf("⚠️ Тема «%s»: %d неудачных апа подряд", name, n)
	}
}

func (s *Scheduler) notifyf(format string, args ...any) {
	s.notifyMu.RLock()
	fn := s.notify
	s.notifyMu.RUnlock()
	if fn == nil {
		return
	}
	if !s.CurrentSettings().Notifications {
		return
	}
	fn(fmt.Sprintf(format, args...))
}
