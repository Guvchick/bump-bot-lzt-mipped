package tg

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/guvchick/bumpbot/internal/forum"
	"github.com/guvchick/bumpbot/internal/storage"
	tele "gopkg.in/telebot.v4"
)

// thrPreview opens the preview card for a thread (fetches live stats).
func (tb *Bot) thrPreview(c tele.Context) error {
	args := c.Args()
	tid, aid := twoIDs(args)
	return tb.renderThreadPreview(c, tid, aid, true)
}

// thrPreviewToggle flips auto-bump from the preview and re-renders it (no live
// refetch — keeps toggling snappy).
func (tb *Bot) thrPreviewToggle(c tele.Context) error {
	args := c.Args()
	tid, aid := twoIDs(args)
	ctx, cancel := tb.dbCtx()
	defer cancel()
	t, err := tb.store.GetThread(ctx, tid)
	if err != nil {
		return err
	}
	t.Enabled = !t.Enabled
	if err := tb.store.UpdateThread(ctx, t); err != nil {
		return err
	}
	return tb.renderThreadPreview(c, tid, aid, false)
}

func (tb *Bot) renderThreadPreview(c tele.Context, tid, aid int64, live bool) error {
	text, markup, err := tb.threadPreviewScreen(tid, aid, live)
	if err != nil {
		return err
	}
	return tb.show(c, text, markup)
}

// threadPreviewScreen renders a thread preview. With live=true it fetches fresh
// stats from the forum; otherwise it shows the latest stored snapshot.
func (tb *Bot) threadPreviewScreen(tid, aid int64, live bool) (string, *tele.ReplyMarkup, error) {
	dctx, dcancel := tb.dbCtx()
	t, err := tb.store.GetThread(dctx, tid)
	if err != nil {
		dcancel()
		return "", nil, err
	}
	acc, _ := tb.store.GetAccount(dctx, t.AccountID)
	dcancel()

	var stats forum.ThreadStats
	if live && acc != nil {
		nctx, ncancel := tb.netCtx()
		if s, ferr := tb.sched.FetchStats(nctx, acc, t); ferr == nil {
			stats = s
			sctx, scancel := tb.dbCtx()
			if s.Title != "" && s.Title != t.Title {
				t.Title = s.Title
				_ = tb.store.UpdateThread(sctx, t)
			}
			_ = tb.store.AddStatsSnapshot(sctx, &storage.StatsSnapshot{
				ThreadID: t.ID, At: time.Now().UTC(), Views: s.Views, Replies: s.Replies,
			})
			scancel()
		}
		ncancel()
	}
	// Fall back to the latest stored snapshot when there's nothing live.
	if stats.Views == nil && stats.Replies == nil {
		sctx, scancel := tb.dbCtx()
		if latest, _ := tb.store.LatestSnapshot(sctx, tid); latest != nil {
			stats.Views, stats.Replies = latest.Views, latest.Replies
		}
		scancel()
	}

	accLabel := "—"
	if acc != nil {
		accLabel = acc.Label
	}
	url := tb.threadURL(t)

	var b strings.Builder
	fmt.Fprintf(&b, "👁 <b>%s</b>\n\n", esc(threadName(t)))
	fmt.Fprintf(&b, "Форум: %s\n", forumName(t.Forum))
	fmt.Fprintf(&b, "Аккаунт: %s\n", esc(accLabel))
	fmt.Fprintf(&b, "Просмотры: %s · Ответы: %s\n", intOrDash(stats.Views), intOrDash(stats.Replies))
	fmt.Fprintf(&b, "Интервал: %d сек\n", t.IntervalSec)
	fmt.Fprintf(&b, "Автоап: %s\n", enabledWord(t.Enabled))
	fmt.Fprintf(&b, "Следующий ап: %s\n", nextBumpLabel(t))
	fmt.Fprintf(&b, "Последний ап: %s", agoLabel(t.LastBumpAt, "никогда"))

	toggle := "▶️ Включить автоап"
	if t.Enabled {
		toggle = "⏸ Выключить автоап"
	}
	m := &tele.ReplyMarkup{}
	rows := []tele.Row{}
	if url != "" {
		rows = append(rows, m.Row(m.URL("🔗 Открыть тему", url)))
	}
	rows = append(rows,
		m.Row(m.Data(toggle, uThrPrevTgl, itoa(tid), itoa(aid))),
		m.Row(m.Data("⬆️ Апнуть сейчас", uThrBump, itoa(tid)), m.Data("📊 Статистика", uThrStats, itoa(tid))),
		m.Row(m.Data("⬅️ К темам аккаунта", uAccThreads, itoa(aid))),
	)
	m.Inline(rows...)
	return b.String(), m, nil
}

// threadURL builds a browser URL for a thread.
func (tb *Bot) threadURL(t *storage.Thread) string {
	switch t.Forum {
	case storage.ForumLolz:
		return "https://lolz.live/threads/" + t.ThreadRef + "/"
	case storage.ForumMipped:
		if strings.HasPrefix(t.ThreadRef, "http") {
			return t.ThreadRef
		}
		return "https://mipped.com/f/threads/" + strings.TrimPrefix(t.ThreadRef, "/")
	}
	return ""
}

func enabledWord(enabled bool) string {
	if enabled {
		return "включён"
	}
	return "выключен"
}

// twoIDs parses the first two callback args as int64 (0 when missing).
func twoIDs(args []string) (int64, int64) {
	var a, b int64
	if len(args) > 0 {
		a, _ = strconv.ParseInt(args[0], 10, 64)
	}
	if len(args) > 1 {
		b, _ = strconv.ParseInt(args[1], 10, 64)
	}
	return a, b
}
