package tg

import (
	"fmt"
	"strings"
	"time"

	"github.com/guvchick/bumpbot/internal/storage"
	tele "gopkg.in/telebot.v4"
)

func (tb *Bot) showStats(c tele.Context) error {
	ctx, cancel := tb.dbCtx()
	defer cancel()
	threads, err := tb.store.ListThreads(ctx)
	if err != nil {
		return err
	}
	var active, paused, waiting int
	now := time.Now()
	for _, t := range threads {
		if !t.Enabled {
			paused++
			continue
		}
		active++
		if t.NextBumpAt != nil && t.NextBumpAt.After(now) {
			waiting++
		}
	}

	m := &tele.ReplyMarkup{}
	var rows []tele.Row
	for _, t := range threads {
		rows = append(rows, m.Row(m.Data(trim(threadName(&t), 30), uStatsThr, itoa(t.ID))))
	}
	rows = append(rows, m.Row(m.Data("⬅️ Назад", uMenu)))
	m.Inline(rows...)

	text := fmt.Sprintf("📊 <b>Статистика</b>\nВсего тем: %d\nАктивно: %d · Ждут: %d · На паузе: %d\n\nВыберите тему для деталей:",
		len(threads), active, waiting, paused)
	return tb.show(c, text, m)
}

func (tb *Bot) thrStats(c tele.Context) error {
	return tb.renderThreadStats(c, firstID(c), uThrView)
}

func (tb *Bot) statsThread(c tele.Context) error {
	return tb.renderThreadStats(c, firstID(c), uStats)
}

func (tb *Bot) renderThreadStats(c tele.Context, id int64, back string) error {
	dctx, dcancel := tb.dbCtx()
	t, err := tb.store.GetThread(dctx, id)
	if err != nil {
		dcancel()
		return err
	}
	acc, _ := tb.store.GetAccount(dctx, t.AccountID)
	dcancel()

	// Best-effort live refresh so the figures are current.
	if acc != nil {
		nctx, ncancel := tb.netCtx()
		if st, err := tb.sched.FetchStats(nctx, acc, t); err == nil {
			sctx, scancel := tb.dbCtx()
			_ = tb.store.AddStatsSnapshot(sctx, &storage.StatsSnapshot{
				ThreadID: t.ID, At: time.Now().UTC(), Views: st.Views, Replies: st.Replies,
			})
			if st.Title != "" && st.Title != t.Title {
				t.Title = st.Title
				_ = tb.store.UpdateThread(sctx, t)
			}
			scancel()
		}
		ncancel()
	}

	ctx, cancel := tb.dbCtx()
	defer cancel()
	latest, _ := tb.store.LatestSnapshot(ctx, id)
	now := time.Now()
	snap24, _ := tb.store.SnapshotAround(ctx, id, now.Add(-24*time.Hour))
	snap7d, _ := tb.store.SnapshotAround(ctx, id, now.Add(-7*24*time.Hour))
	bumps, _ := tb.store.CountSuccessfulBumps(ctx, id)
	snaps, _ := tb.store.ListSnapshots(ctx, id, now.Add(-7*24*time.Hour), 60)

	var b strings.Builder
	fmt.Fprintf(&b, "📊 <b>%s</b>\n\n", esc(threadName(t)))
	if latest != nil {
		fmt.Fprintf(&b, "Просмотры: %s\n", intOrDash(latest.Views))
		fmt.Fprintf(&b, "Ответы: %s\n", intOrDash(latest.Replies))
		fmt.Fprintf(&b, "Δ просмотров 24ч: %s · 7д: %s\n",
			deltaViews(latest, snap24), deltaViews(latest, snap7d))
	} else {
		b.WriteString("Снимков статистики пока нет.\n")
	}
	fmt.Fprintf(&b, "Успешных апов: %d\n", bumps)
	fmt.Fprintf(&b, "Последний ап: %s\n", agoLabel(t.LastBumpAt, "никогда"))

	if spark := viewsSparkline(snaps); spark != "" {
		fmt.Fprintf(&b, "\nРост просмотров (7д):\n<code>%s</code>", spark)
	}

	m := &tele.ReplyMarkup{}
	m.Inline(
		m.Row(m.Data("🔄 Обновить", uStatsThr, itoa(id))),
		m.Row(m.Data("⬅️ Назад", back, backArgs(back, id)...)),
	)
	return tb.show(c, b.String(), m)
}

func backArgs(back string, id int64) []string {
	if back == uThrView {
		return []string{itoa(id)}
	}
	return nil
}

func deltaViews(now, then *storage.StatsSnapshot) string {
	if now == nil || then == nil {
		return "—"
	}
	return delta(now.Views, then.Views)
}

func viewsSparkline(snaps []storage.StatsSnapshot) string {
	var vals []int
	for _, s := range snaps {
		if s.Views != nil {
			vals = append(vals, *s.Views)
		}
	}
	if len(vals) < 2 {
		return ""
	}
	return sparkline(vals)
}
