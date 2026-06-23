package tg

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/guvchick/bumpbot/internal/storage"
	tele "gopkg.in/telebot.v4"
)

func (tb *Bot) showThreads(c tele.Context) error {
	ctx, cancel := tb.dbCtx()
	defer cancel()
	threads, err := tb.store.ListThreads(ctx)
	if err != nil {
		return err
	}
	m := &tele.ReplyMarkup{}
	var rows []tele.Row
	for _, t := range threads {
		label := fmt.Sprintf("%s %s · %s", enabledIcon(t.Enabled), trim(threadName(&t), 22), nextBumpLabel(&t))
		rows = append(rows, m.Row(m.Data(label, uThrView, itoa(t.ID))))
	}
	rows = append(rows, m.Row(m.Data("➕ Добавить тему", uThrAdd)))
	rows = append(rows, m.Row(m.Data("⬅️ Назад", uMenu)))
	m.Inline(rows...)

	text := fmt.Sprintf("📌 <b>Темы</b> (%d)", len(threads))
	if len(threads) == 0 {
		text += "\n\nПока нет тем. Нажмите «Добавить тему»."
	}
	return tb.show(c, text, m)
}

func (tb *Bot) thrAdd(c tele.Context) error {
	ctx, cancel := tb.dbCtx()
	defer cancel()
	accs, err := tb.store.ListAccounts(ctx)
	if err != nil {
		return err
	}
	if len(accs) == 0 {
		m := &tele.ReplyMarkup{}
		m.Inline(m.Row(m.Data("👤 К аккаунтам", uAccounts)), m.Row(m.Data("⬅️ Назад", uThreads)))
		return tb.show(c, "Сначала добавьте аккаунт, к которому привязать тему.", m)
	}
	m := &tele.ReplyMarkup{}
	var rows []tele.Row
	for _, a := range accs {
		rows = append(rows, m.Row(m.Data(fmt.Sprintf("%s (%s)", trim(a.Label, 24), forumName(a.Forum)), uThrAddAcc, itoa(a.ID))))
	}
	rows = append(rows, m.Row(m.Data("⬅️ Назад", uThreads)))
	m.Inline(rows...)
	return tb.show(c, "Выберите аккаунт для новой темы:", m)
}

func (tb *Bot) thrAddAcc(c tele.Context) error {
	id := firstID(c)
	ctx, cancel := tb.dbCtx()
	a, err := tb.store.GetAccount(ctx, id)
	cancel()
	if err != nil {
		return err
	}
	tb.fsm.set(c.Sender().ID, &state{step: stepThrRef, accountID: id, forum: a.Forum})
	prompt := "Введите числовой <b>thread_id</b> темы Lolzteam:"
	if a.Forum == storage.ForumMipped {
		prompt = "Введите полный <b>URL темы</b> Mipped:"
	}
	m := &tele.ReplyMarkup{}
	m.Inline(cancelRow(m, uThreads))
	return tb.show(c, prompt, m)
}

func (tb *Bot) inputThrRef(c tele.Context, st *state) error {
	ref := strings.TrimSpace(c.Text())
	if ref == "" {
		return c.Send("Пустое значение. Введите ещё раз:")
	}
	if st.forum == storage.ForumLolz {
		if _, err := strconv.Atoi(ref); err != nil {
			return c.Send("Для Lolzteam нужен числовой thread_id. Попробуйте ещё раз:")
		}
	} else if !strings.Contains(ref, "/threads/") && !strings.Contains(ref, ".") {
		return c.Send("Похоже, это не URL темы Mipped. Введите ссылку вида https://mipped.com/f/threads/slug.12345/")
	}
	st.ref = ref
	st.step = stepThrInterval
	tb.fsm.set(c.Sender().ID, st)
	def := tb.sched.DefaultInterval(st.forum)
	return c.Send(fmt.Sprintf("Введите интервал апа в секундах.\nОтправьте «-» чтобы взять значение по умолчанию: <b>%d</b> сек.", def), tele.ModeHTML)
}

func (tb *Bot) inputThrInterval(c tele.Context, st *state) error {
	txt := strings.TrimSpace(c.Text())
	interval := tb.sched.DefaultInterval(st.forum)
	if txt != "-" {
		n, err := strconv.Atoi(txt)
		if err != nil || n < 60 {
			return c.Send("Введите число секунд (минимум 60) или «-» для значения по умолчанию:")
		}
		interval = n
	}
	tb.fsm.clear(c.Sender().ID)

	t := &storage.Thread{
		AccountID:   st.accountID,
		Forum:       st.forum,
		ThreadRef:   st.ref,
		IntervalSec: interval,
		Enabled:     true,
		// NextBumpAt left nil -> the scheduler treats it as due soon.
	}
	dctx, dcancel := tb.dbCtx()
	if err := tb.store.CreateThread(dctx, t); err != nil {
		dcancel()
		return err
	}
	dcancel()

	if err := c.Send("✅ Тема добавлена. Подтягиваю заголовок…"); err != nil {
		return err
	}
	// Best-effort title fetch.
	nctx, ncancel := tb.netCtx()
	defer ncancel()
	if acc, err := tb.store.GetAccount(nctx, t.AccountID); err == nil {
		if stats, err := tb.sched.FetchStats(nctx, acc, t); err == nil && stats.Title != "" {
			t.Title = stats.Title
			dctx2, dcancel2 := tb.dbCtx()
			_ = tb.store.UpdateThread(dctx2, t)
			dcancel2()
		}
	}
	return tb.sendThreadByID(c, t.ID, "")
}

func (tb *Bot) thrView(c tele.Context) error {
	return tb.renderThread(c, firstID(c), "")
}

func (tb *Bot) sendThreadByID(c tele.Context, id int64, note string) error {
	text, markup, err := tb.threadScreen(id, note)
	if err != nil {
		return err
	}
	return c.Send(text, markup, tele.ModeHTML)
}

func (tb *Bot) renderThread(c tele.Context, id int64, note string) error {
	text, markup, err := tb.threadScreen(id, note)
	if err != nil {
		return err
	}
	return tb.show(c, text, markup)
}

func (tb *Bot) threadScreen(id int64, note string) (string, *tele.ReplyMarkup, error) {
	ctx, cancel := tb.dbCtx()
	defer cancel()
	t, err := tb.store.GetThread(ctx, id)
	if err != nil {
		return "", nil, err
	}
	acc, _ := tb.store.GetAccount(ctx, t.AccountID)
	accLabel := "—"
	if acc != nil {
		accLabel = acc.Label
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s <b>%s</b>\n", enabledIcon(t.Enabled), esc(threadName(t)))
	fmt.Fprintf(&b, "Форум: %s\n", forumName(t.Forum))
	fmt.Fprintf(&b, "Аккаунт: %s\n", esc(accLabel))
	fmt.Fprintf(&b, "Ref: <code>%s</code>\n", esc(trim(t.ThreadRef, 60)))
	fmt.Fprintf(&b, "Интервал: %d сек\n", t.IntervalSec)
	fmt.Fprintf(&b, "Автоап: %s\n", map[bool]string{true: "включён", false: "выключен"}[t.Enabled])
	fmt.Fprintf(&b, "Следующий ап: %s\n", nextBumpLabel(t))
	fmt.Fprintf(&b, "Последний ап: %s\n", agoLabel(t.LastBumpAt, "никогда"))
	if note != "" {
		fmt.Fprintf(&b, "\n%s", note)
	}

	toggleLabel := "▶️ Включить автоап"
	if t.Enabled {
		toggleLabel = "⏸ Выключить автоап"
	}
	m := &tele.ReplyMarkup{}
	m.Inline(
		m.Row(m.Data("⬆️ Апнуть сейчас", uThrBump, itoa(id))),
		m.Row(m.Data(toggleLabel, uThrToggle, itoa(id)), m.Data("⏱ Интервал", uThrInt, itoa(id))),
		m.Row(m.Data("📊 Статистика", uThrStats, itoa(id)), m.Data("🗑 Удалить", uThrDel, itoa(id))),
		m.Row(m.Data("⬅️ Назад", uThreads)),
	)
	return b.String(), m, nil
}

func (tb *Bot) thrBump(c tele.Context) error {
	id := firstID(c)
	_ = c.Respond(&tele.CallbackResponse{Text: "Апаю…"})
	nctx, ncancel := tb.netCtx()
	res, err := tb.sched.BumpNow(nctx, id)
	ncancel()

	var note string
	switch {
	case err != nil:
		note = "❌ Ошибка: " + esc(err.Error())
	case res.OK:
		note = "✅ Тема поднята."
	case res.RetryAfter > 0:
		note = "⏳ Рано: " + esc(trim(res.Message, 120))
	default:
		note = "⚠️ " + esc(trim(res.Message, 120))
	}
	return tb.renderThread(c, id, note)
}

func (tb *Bot) thrToggle(c tele.Context) error {
	id := firstID(c)
	ctx, cancel := tb.dbCtx()
	defer cancel()
	t, err := tb.store.GetThread(ctx, id)
	if err != nil {
		return err
	}
	t.Enabled = !t.Enabled
	if err := tb.store.UpdateThread(ctx, t); err != nil {
		return err
	}
	note := "⏸ Автоап выключен."
	if t.Enabled {
		note = "✅ Автоап включён."
	}
	return tb.renderThread(c, id, note)
}

func (tb *Bot) thrIntervalPrompt(c tele.Context) error {
	id := firstID(c)
	tb.fsm.set(c.Sender().ID, &state{step: stepEditInterval, threadID: id})
	m := &tele.ReplyMarkup{}
	m.Inline(cancelRow(m, uThrView, itoa(id)))
	return tb.show(c, "Введите новый интервал апа в секундах (минимум 60):", m)
}

func (tb *Bot) inputEditInterval(c tele.Context, st *state) error {
	n, err := strconv.Atoi(strings.TrimSpace(c.Text()))
	if err != nil || n < 60 {
		return c.Send("Введите число секунд (минимум 60):")
	}
	tb.fsm.clear(c.Sender().ID)
	ctx, cancel := tb.dbCtx()
	defer cancel()
	t, err := tb.store.GetThread(ctx, st.threadID)
	if err != nil {
		return err
	}
	t.IntervalSec = n
	if err := tb.store.UpdateThread(ctx, t); err != nil {
		return err
	}
	return tb.sendThreadByID(c, st.threadID, fmt.Sprintf("✅ Интервал обновлён: %d сек.", n))
}

func (tb *Bot) thrDelConfirm(c tele.Context) error {
	id := firstID(c)
	m := &tele.ReplyMarkup{}
	m.Inline(m.Row(m.Data("✅ Да, удалить", uThrDelOK, itoa(id)), m.Data("⬅️ Нет", uThrView, itoa(id))))
	return tb.show(c, "Удалить тему и её историю апов/статистики?", m)
}

func (tb *Bot) thrDelete(c tele.Context) error {
	ctx, cancel := tb.dbCtx()
	defer cancel()
	if err := tb.store.DeleteThread(ctx, firstID(c)); err != nil {
		return err
	}
	return tb.showThreads(c)
}
