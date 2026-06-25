// Package tg implements the Telegram control panel: inline-button navigation,
// an FSM for multi-step text input, and owner-only access.
package tg

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/guvchick/bumpbot/internal/config"
	"github.com/guvchick/bumpbot/internal/crypto"
	"github.com/guvchick/bumpbot/internal/scheduler"
	"github.com/guvchick/bumpbot/internal/storage"
	tele "gopkg.in/telebot.v4"
)

// Callback unique identifiers. Must match `[-\w]+` (telebot's cbackRx).
const (
	uMenu       = "menu"
	uAccounts   = "accs"
	uAccView    = "acc"
	uAccAdd     = "accadd"
	uAccForum   = "accaddf"
	uAccCheck   = "accchk"
	uAccDel     = "accdel"
	uAccDelOK   = "accdely"
	uAccProxy   = "accpx"
	uAccCookies = "accck"
	uAccImport  = "accimp"
	uAccThreads = "accthr"
	uAccAllOn   = "accon"
	uAccAllOff  = "accoff"
	uThrQTgl    = "thrqt"
	uThrPrev    = "thrpv"
	uThrPrevTgl = "thrpvt"

	uThreads   = "thrs"
	uThrView   = "thr"
	uThrAdd    = "thradd"
	uThrAddAcc = "thrada"
	uThrBump   = "thrbmp"
	uThrToggle = "thrtgl"
	uThrInt    = "thrint"
	uThrStats  = "thrst"
	uThrDel    = "thrdel"
	uThrDelOK  = "thrdely"

	uStats    = "stats"
	uStatsThr = "statst"
	uLogs     = "logs"

	uSettings = "set"
	uSetEdit  = "sete"
	uNotifTgl = "setn"
	uNoop     = "noop"
)

// Bot wraps the telebot.Bot and the app dependencies.
type Bot struct {
	bot    *tele.Bot
	cfg    *config.Config
	store  storage.Storage
	sched  *scheduler.Scheduler
	crypto *crypto.Crypto
	log    *slog.Logger
	fsm    *fsmStore
}

// New constructs the Telegram bot and registers all handlers.
func New(cfg *config.Config, store storage.Storage, sched *scheduler.Scheduler, cr *crypto.Crypto, log *slog.Logger) (*Bot, error) {
	pref := tele.Settings{
		Token:  cfg.TelegramToken,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
		OnError: func(err error, c tele.Context) {
			log.Error("telegram handler", "err", err)
		},
	}
	b, err := tele.NewBot(pref)
	if err != nil {
		return nil, err
	}
	tb := &Bot{bot: b, cfg: cfg, store: store, sched: sched, crypto: cr, log: log, fsm: newFSM()}
	tb.setup()
	tb.registerNotifier()
	return tb, nil
}

// Start begins long-polling (blocking).
func (tb *Bot) Start() { tb.bot.Start() }

// Stop halts the bot.
func (tb *Bot) Stop() { tb.bot.Stop() }

func (tb *Bot) registerNotifier() {
	tb.sched.SetNotifier(func(msg string) {
		for _, id := range tb.cfg.OwnerIDs {
			if _, err := tb.bot.Send(tele.ChatID(id), msg); err != nil {
				tb.log.Debug("notify send", "id", id, "err", err)
			}
		}
	})
}

func (tb *Bot) setup() {
	// Owner-only whitelist.
	tb.bot.Use(func(next tele.HandlerFunc) tele.HandlerFunc {
		return func(c tele.Context) error {
			s := c.Sender()
			if s == nil || !tb.cfg.IsOwner(s.ID) {
				if c.Callback() != nil {
					_ = c.Respond(&tele.CallbackResponse{Text: "Нет доступа"})
				}
				return nil
			}
			return next(c)
		}
	})

	tb.bot.Handle("/start", tb.showMain)
	tb.bot.Handle("/menu", tb.showMain)
	tb.bot.Handle(tele.OnText, tb.onText)
	tb.bot.Handle(tele.OnCallback, func(c tele.Context) error {
		// Catch-all for stale/unknown callbacks.
		return c.Respond(&tele.CallbackResponse{Text: "Кнопка устарела"})
	})

	// Navigation / actions.
	tb.handleBtn(uMenu, tb.showMain)
	tb.handleBtn(uNoop, func(c tele.Context) error { return nil })

	tb.handleBtn(uAccounts, tb.showAccounts)
	tb.handleBtn(uAccAdd, tb.accAdd)
	tb.handleBtn(uAccForum, tb.accAddForum)
	tb.handleBtn(uAccView, tb.accView)
	tb.handleBtn(uAccCheck, tb.accCheck)
	tb.handleBtn(uAccDel, tb.accDelConfirm)
	tb.handleBtn(uAccDelOK, tb.accDelete)
	tb.handleBtn(uAccProxy, tb.accProxyPrompt)
	tb.handleBtn(uAccCookies, tb.accCookiesPrompt)
	tb.handleBtn(uAccImport, tb.accImport)
	tb.handleBtn(uAccThreads, tb.accThreads)
	tb.handleBtn(uAccAllOn, tb.accAllOn)
	tb.handleBtn(uAccAllOff, tb.accAllOff)
	tb.handleBtn(uThrQTgl, tb.thrQuickToggle)
	tb.handleBtn(uThrPrev, tb.thrPreview)
	tb.handleBtn(uThrPrevTgl, tb.thrPreviewToggle)

	tb.handleBtn(uThreads, tb.showThreads)
	tb.handleBtn(uThrAdd, tb.thrAdd)
	tb.handleBtn(uThrAddAcc, tb.thrAddAcc)
	tb.handleBtn(uThrView, tb.thrView)
	tb.handleBtn(uThrBump, tb.thrBump)
	tb.handleBtn(uThrToggle, tb.thrToggle)
	tb.handleBtn(uThrInt, tb.thrIntervalPrompt)
	tb.handleBtn(uThrStats, tb.thrStats)
	tb.handleBtn(uThrDel, tb.thrDelConfirm)
	tb.handleBtn(uThrDelOK, tb.thrDelete)

	tb.handleBtn(uStats, tb.showStats)
	tb.handleBtn(uStatsThr, tb.statsThread)
	tb.handleBtn(uLogs, tb.showLogs)

	tb.handleBtn(uSettings, tb.showSettings)
	tb.handleBtn(uSetEdit, tb.settingEditPrompt)
	tb.handleBtn(uNotifTgl, tb.settingToggleNotif)
}

// handleBtn registers a callback handler. The wrapper cancels any pending FSM
// input (pressing a button abandons text entry) and always answers the callback.
func (tb *Bot) handleBtn(unique string, h tele.HandlerFunc) {
	tb.bot.Handle(&tele.Btn{Unique: unique}, func(c tele.Context) error {
		if s := c.Sender(); s != nil {
			tb.fsm.clear(s.ID)
		}
		err := h(c)
		_ = c.Respond()
		return err
	})
}

// show edits the current message (callback) or sends a new one (command/text).
func (tb *Bot) show(c tele.Context, text string, markup *tele.ReplyMarkup) error {
	if c.Callback() != nil {
		err := c.Edit(text, markup, tele.ModeHTML)
		if err != nil && strings.Contains(err.Error(), "not modified") {
			return nil
		}
		return err
	}
	return c.Send(text, markup, tele.ModeHTML)
}

// netCtx returns a context with a timeout suited to a forum network round-trip.
func (tb *Bot) netCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 90*time.Second)
}

// dbCtx returns a short context for local DB work.
func (tb *Bot) dbCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// importCtx allows enough time for a paginated, rate-limited thread import.
func (tb *Bot) importCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Minute)
}

// showMain renders the top-level menu.
func (tb *Bot) showMain(c tele.Context) error {
	if s := c.Sender(); s != nil {
		tb.fsm.clear(s.ID)
	}
	m := &tele.ReplyMarkup{}
	m.Inline(
		m.Row(m.Data("👤 Аккаунты", uAccounts), m.Data("📌 Темы", uThreads)),
		m.Row(m.Data("📊 Статистика", uStats), m.Data("🧾 Логи", uLogs)),
		m.Row(m.Data("⚙️ Настройки", uSettings)),
	)
	return tb.show(c, "🤖 <b>Бамп-бот</b>\nГлавное меню — выберите раздел.", m)
}
