// Command bot runs the Telegram-controlled forum bump bot.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/guvchick/bumpbot/internal/config"
	"github.com/guvchick/bumpbot/internal/crypto"
	"github.com/guvchick/bumpbot/internal/forum"
	"github.com/guvchick/bumpbot/internal/scheduler"
	"github.com/guvchick/bumpbot/internal/storage"
	"github.com/guvchick/bumpbot/internal/tg"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	cr, err := crypto.New(cfg.EncryptionKey)
	if err != nil {
		return err
	}

	store, err := storage.OpenSQLite(cfg.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := store.Migrate(rootCtx); err != nil {
		return err
	}

	forums := map[string]forum.Forum{
		storage.ForumLolz:   forum.NewLolz(),
		storage.ForumMipped: forum.NewMipped(),
	}

	sched := scheduler.New(store, cr, forums, cfg, logger)
	if err := sched.SeedSettings(rootCtx); err != nil {
		return err
	}

	bot, err := tg.New(cfg, store, sched, cr, logger)
	if err != nil {
		return err
	}

	sched.Run(rootCtx)

	// Stop long-polling when a shutdown signal arrives.
	go func() {
		<-rootCtx.Done()
		logger.Info("shutting down")
		bot.Stop()
	}()

	logger.Info("bot started", "owners", cfg.OwnerIDs, "db", cfg.DBPath)
	bot.Start() // blocks until Stop()
	return nil
}
