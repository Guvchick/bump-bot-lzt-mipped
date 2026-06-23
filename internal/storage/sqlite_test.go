package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *SQLite {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestAccountAndThreadCRUD(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	acc := &Account{Forum: ForumLolz, Label: "main", SecretEnc: []byte("enc"), Status: StatusOK}
	if err := s.CreateAccount(ctx, acc); err != nil {
		t.Fatal(err)
	}
	if acc.ID == 0 {
		t.Fatal("account id not set")
	}

	got, err := s.GetAccount(ctx, acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Label != "main" || got.Forum != ForumLolz {
		t.Fatalf("unexpected account: %+v", got)
	}

	th := &Thread{AccountID: acc.ID, Forum: ForumLolz, ThreadRef: "12345", IntervalSec: 14400, Enabled: true}
	if err := s.CreateThread(ctx, th); err != nil {
		t.Fatal(err)
	}
	n, _ := s.CountThreads(ctx, acc.ID)
	if n != 1 {
		t.Fatalf("CountThreads = %d, want 1", n)
	}

	// Deleting the account cascades to its threads.
	if err := s.DeleteAccount(ctx, acc.ID); err != nil {
		t.Fatal(err)
	}
	if threads, _ := s.ListThreads(ctx); len(threads) != 0 {
		t.Fatalf("expected cascade delete, got %d threads", len(threads))
	}
}

func TestListDueThreadsRespectsSchedule(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	acc := &Account{Forum: ForumMipped, Label: "m", SecretEnc: []byte("x")}
	_ = s.CreateAccount(ctx, acc)

	now := time.Now().UTC()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	dueByNull := &Thread{AccountID: acc.ID, Forum: ForumMipped, ThreadRef: "a.1", IntervalSec: 86400, Enabled: true}
	duePast := &Thread{AccountID: acc.ID, Forum: ForumMipped, ThreadRef: "b.2", IntervalSec: 86400, Enabled: true, NextBumpAt: &past}
	notDue := &Thread{AccountID: acc.ID, Forum: ForumMipped, ThreadRef: "c.3", IntervalSec: 86400, Enabled: true, NextBumpAt: &future}
	disabled := &Thread{AccountID: acc.ID, Forum: ForumMipped, ThreadRef: "d.4", IntervalSec: 86400, Enabled: false, NextBumpAt: &past}
	for _, th := range []*Thread{dueByNull, duePast, notDue, disabled} {
		if err := s.CreateThread(ctx, th); err != nil {
			t.Fatal(err)
		}
	}

	due, err := s.ListDueThreads(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 2 {
		t.Fatalf("ListDueThreads = %d, want 2 (null + past, not future or disabled)", len(due))
	}
	for _, th := range due {
		if th.ID == notDue.ID || th.ID == disabled.ID {
			t.Fatalf("thread %d should not be due", th.ID)
		}
	}
}

func TestSettingsAndLogs(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, ok, _ := s.GetSetting(ctx, "x"); ok {
		t.Fatal("unexpected setting")
	}
	if err := s.SetSetting(ctx, "x", "5"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "x", "7"); err != nil { // upsert
		t.Fatal(err)
	}
	if v, ok, _ := s.GetSetting(ctx, "x"); !ok || v != "7" {
		t.Fatalf("GetSetting = %q,%v want 7,true", v, ok)
	}

	acc := &Account{Forum: ForumLolz, Label: "l", SecretEnc: []byte("x")}
	_ = s.CreateAccount(ctx, acc)
	th := &Thread{AccountID: acc.ID, Forum: ForumLolz, ThreadRef: "1", IntervalSec: 100, Enabled: true}
	_ = s.CreateThread(ctx, th)

	next := time.Now().Add(time.Hour).UTC()
	_ = s.AddBumpLog(ctx, &BumpLog{ThreadID: th.ID, OK: true, Message: "bumped", NextAt: &next})
	_ = s.AddBumpLog(ctx, &BumpLog{ThreadID: th.ID, OK: false, Message: "too early"})

	logs, _ := s.ListBumpLogs(ctx, th.ID, 10)
	if len(logs) != 2 {
		t.Fatalf("logs = %d, want 2", len(logs))
	}
	if ok, _ := s.CountSuccessfulBumps(ctx, th.ID); ok != 1 {
		t.Fatalf("CountSuccessfulBumps = %d, want 1", ok)
	}
}
