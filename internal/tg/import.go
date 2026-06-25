package tg

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/guvchick/bumpbot/internal/storage"
	tele "gopkg.in/telebot.v4"
)

// maxThreadButtons caps how many threads we render on one manage screen
// (Telegram limits inline keyboard size / message length).
const maxThreadButtons = 40

// accImport runs auto-import for an account, then shows its manage screen.
func (tb *Bot) accImport(c tele.Context) error {
	id := firstID(c)
	_ = c.Respond(&tele.CallbackResponse{Text: "Импортирую темы…"})

	dctx, dcancel := tb.dbCtx()
	a, err := tb.store.GetAccount(dctx, id)
	dcancel()
	if err != nil {
		return err
	}
	note := tb.runImport(a)
	return tb.renderAccountThreads(c, id, note)
}

// runImport performs the import and returns a human-readable note line.
func (tb *Bot) runImport(a *storage.Account) string {
	ctx, cancel := tb.importCtx()
	defer cancel()
	added, found, err := tb.sched.ImportThreads(ctx, a)
	switch {
	case err != nil:
		return "⚠️ Импорт не удался: " + esc(err.Error())
	case found == 0:
		return "📥 Тем для импорта не найдено."
	default:
		return fmt.Sprintf("📥 Найдено %d, добавлено новых %d (выключены — отметьте нужные ниже).", found, added)
	}
}

func (tb *Bot) accThreads(c tele.Context) error {
	return tb.renderAccountThreads(c, firstID(c), "")
}

func (tb *Bot) renderAccountThreads(c tele.Context, id int64, note string) error {
	text, markup, err := tb.accountThreadsScreen(id, note)
	if err != nil {
		return err
	}
	return tb.show(c, text, markup)
}

// sendAccountThreads forces a fresh send (used after FSM input).
func (tb *Bot) sendAccountThreads(c tele.Context, id int64, note string) error {
	text, markup, err := tb.accountThreadsScreen(id, note)
	if err != nil {
		return err
	}
	return c.Send(text, markup, tele.ModeHTML)
}

func (tb *Bot) accountThreadsScreen(id int64, note string) (string, *tele.ReplyMarkup, error) {
	ctx, cancel := tb.dbCtx()
	defer cancel()
	a, err := tb.store.GetAccount(ctx, id)
	if err != nil {
		return "", nil, err
	}
	threads, err := tb.store.ListThreadsByAccount(ctx, id)
	if err != nil {
		return "", nil, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "📂 <b>Темы аккаунта «%s»</b> (%d)\n", esc(a.Label), len(threads))
	b.WriteString("Тап по 🟢/⏸ — вкл/выкл автоап, по названию — предпросмотр.")
	if note != "" {
		fmt.Fprintf(&b, "\n\n%s", note)
	}
	if len(threads) == 0 {
		b.WriteString("\n\nПусто. Нажмите «📥 Импорт» или добавьте тему вручную в разделе 📌 Темы.")
	}

	m := &tele.ReplyMarkup{}
	var rows []tele.Row
	shown := threads
	if len(shown) > maxThreadButtons {
		shown = shown[:maxThreadButtons]
	}
	for i := range shown {
		t := shown[i]
		rows = append(rows, m.Row(
			m.Data(enabledIcon(t.Enabled), uThrQTgl, itoa(t.ID), itoa(id)),
			m.Data(trim(threadName(&t), 30), uThrPrev, itoa(t.ID), itoa(id)),
		))
	}
	if len(threads) > maxThreadButtons {
		rows = append(rows, m.Row(m.Data(fmt.Sprintf("… показаны первые %d из %d", maxThreadButtons, len(threads)), uNoop)))
	}
	if len(threads) > 0 {
		rows = append(rows, m.Row(
			m.Data("🟢 Включить все", uAccAllOn, itoa(id)),
			m.Data("⚪ Выключить все", uAccAllOff, itoa(id)),
		))
	}
	rows = append(rows,
		m.Row(m.Data("📥 Импорт", uAccImport, itoa(id))),
		m.Row(m.Data("⬅️ К аккаунту", uAccView, itoa(id))),
	)
	m.Inline(rows...)
	return b.String(), m, nil
}

// thrQuickToggle flips a thread's auto-bump from the manage list and re-renders.
func (tb *Bot) thrQuickToggle(c tele.Context) error {
	args := c.Args()
	if len(args) < 2 {
		return nil
	}
	tid, _ := strconv.ParseInt(args[0], 10, 64)
	aid, _ := strconv.ParseInt(args[1], 10, 64)

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
	return tb.renderAccountThreads(c, aid, "")
}

func (tb *Bot) accAllOn(c tele.Context) error  { return tb.accSetAll(c, true) }
func (tb *Bot) accAllOff(c tele.Context) error { return tb.accSetAll(c, false) }

func (tb *Bot) accSetAll(c tele.Context, enabled bool) error {
	id := firstID(c)
	ctx, cancel := tb.dbCtx()
	defer cancel()
	threads, err := tb.store.ListThreadsByAccount(ctx, id)
	if err != nil {
		return err
	}
	for i := range threads {
		if threads[i].Enabled != enabled {
			threads[i].Enabled = enabled
			_ = tb.store.UpdateThread(ctx, &threads[i])
		}
	}
	note := "⚪ Автоап выключен у всех тем."
	if enabled {
		note = "🟢 Автоап включён у всех тем."
	}
	return tb.renderAccountThreads(c, id, note)
}
