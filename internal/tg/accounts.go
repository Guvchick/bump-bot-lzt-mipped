package tg

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/guvchick/bumpbot/internal/forum"
	"github.com/guvchick/bumpbot/internal/storage"
	tele "gopkg.in/telebot.v4"
)

func (tb *Bot) showAccounts(c tele.Context) error {
	ctx, cancel := tb.dbCtx()
	defer cancel()
	accs, err := tb.store.ListAccounts(ctx)
	if err != nil {
		return err
	}
	m := &tele.ReplyMarkup{}
	var rows []tele.Row
	for _, a := range accs {
		n, _ := tb.store.CountThreads(ctx, a.ID)
		label := fmt.Sprintf("%s %s · %s · %d тем", statusEmoji(a.Status), trim(a.Label, 24), forumName(a.Forum), n)
		rows = append(rows, m.Row(m.Data(label, uAccView, itoa(a.ID))))
	}
	rows = append(rows, m.Row(m.Data("➕ Добавить", uAccAdd)))
	rows = append(rows, m.Row(m.Data("⬅️ Назад", uMenu)))
	m.Inline(rows...)

	text := fmt.Sprintf("👤 <b>Аккаунты</b> (%d)", len(accs))
	if len(accs) == 0 {
		text += "\n\nПока нет аккаунтов. Нажмите «Добавить»."
	}
	return tb.show(c, text, m)
}

func (tb *Bot) accAdd(c tele.Context) error {
	m := &tele.ReplyMarkup{}
	m.Inline(
		m.Row(m.Data("Lolzteam", uAccForum, storage.ForumLolz), m.Data("Mipped", uAccForum, storage.ForumMipped)),
		m.Row(m.Data("⬅️ Назад", uAccounts)),
	)
	return tb.show(c, "Выберите форум для нового аккаунта:", m)
}

func (tb *Bot) accAddForum(c tele.Context) error {
	forumName := firstArg(c)
	if forumName != storage.ForumLolz && forumName != storage.ForumMipped {
		return tb.accAdd(c)
	}
	tb.fsm.set(c.Sender().ID, &state{step: stepAccLabel, forum: forumName})
	m := &tele.ReplyMarkup{}
	m.Inline(cancelRow(m, uAccounts))
	return tb.show(c, "Введите название (метку) для аккаунта:", m)
}

func (tb *Bot) inputAccLabel(c tele.Context, st *state) error {
	st.label = strings.TrimSpace(c.Text())
	if st.label == "" {
		return c.Send("Метка не может быть пустой. Введите ещё раз:")
	}
	if st.forum == storage.ForumLolz {
		st.step = stepAccToken
		tb.fsm.set(c.Sender().ID, st)
		return c.Send("Отправьте API-токен Lolzteam.\n<i>Сообщение с токеном будет удалено.</i>", tele.ModeHTML)
	}
	st.step = stepAccLogin
	tb.fsm.set(c.Sender().ID, st)
	return c.Send("Отправьте логин Mipped:")
}

func (tb *Bot) inputAccToken(c tele.Context, st *state) error {
	token := strings.TrimSpace(c.Text())
	_ = c.Delete() // remove the secret from chat history
	if token == "" {
		return c.Send("Пустой токен. Введите ещё раз:")
	}
	enc, err := tb.crypto.Encrypt([]byte(token))
	if err != nil {
		return err
	}
	acc := &storage.Account{Forum: storage.ForumLolz, Label: st.label, SecretEnc: enc, Status: storage.StatusUnknown}
	if err := tb.persistAndCheck(c, acc); err != nil {
		return err
	}
	return nil
}

func (tb *Bot) inputAccLogin(c tele.Context, st *state) error {
	st.login = strings.TrimSpace(c.Text())
	if st.login == "" {
		return c.Send("Пустой логин. Введите ещё раз:")
	}
	st.step = stepAccPassword
	tb.fsm.set(c.Sender().ID, st)
	return c.Send("Отправьте пароль Mipped.\n<i>Сообщение с паролем будет удалено.</i>", tele.ModeHTML)
}

func (tb *Bot) inputAccPassword(c tele.Context, st *state) error {
	password := strings.TrimSpace(c.Text())
	_ = c.Delete() // remove the secret from chat history
	if password == "" {
		return c.Send("Пустой пароль. Введите ещё раз:")
	}
	creds, _ := json.Marshal(map[string]string{"login": st.login, "password": password})
	enc, err := tb.crypto.Encrypt(creds)
	if err != nil {
		return err
	}
	acc := &storage.Account{Forum: storage.ForumMipped, Label: st.label, SecretEnc: enc, Status: storage.StatusUnknown}
	return tb.persistAndCheck(c, acc)
}

// persistAndCheck stores a new account, runs CheckAuth, and shows the result.
func (tb *Bot) persistAndCheck(c tele.Context, acc *storage.Account) error {
	tb.fsm.clear(c.Sender().ID)
	dctx, dcancel := tb.dbCtx()
	if err := tb.store.CreateAccount(dctx, acc); err != nil {
		dcancel()
		return err
	}
	dcancel()

	if err := c.Send(fmt.Sprintf("✅ Аккаунт «%s» сохранён. Проверяю авторизацию…", esc(acc.Label)), tele.ModeHTML); err != nil {
		return err
	}
	nctx, ncancel := tb.netCtx()
	defer ncancel()
	if err := tb.sched.CheckAuth(nctx, acc); err != nil {
		return tb.sendAccountByID(c, acc.ID, "🔴 Авторизация не прошла: "+esc(err.Error()))
	}
	return tb.sendAccountByID(c, acc.ID, "🟢 Авторизация успешна.")
}

func (tb *Bot) accView(c tele.Context) error {
	return tb.renderAccount(c, firstID(c), "")
}

// sendAccountByID forces a fresh send (used after FSM input where editing the
// user's message is impossible).
func (tb *Bot) sendAccountByID(c tele.Context, id int64, note string) error {
	text, markup, err := tb.accountScreen(id, note)
	if err != nil {
		return err
	}
	return c.Send(text, markup, tele.ModeHTML)
}

func (tb *Bot) renderAccount(c tele.Context, id int64, note string) error {
	text, markup, err := tb.accountScreen(id, note)
	if err != nil {
		return err
	}
	return tb.show(c, text, markup)
}

func (tb *Bot) accountScreen(id int64, note string) (string, *tele.ReplyMarkup, error) {
	ctx, cancel := tb.dbCtx()
	defer cancel()
	a, err := tb.store.GetAccount(ctx, id)
	if err != nil {
		return "", nil, err
	}
	n, _ := tb.store.CountThreads(ctx, id)
	proxy := a.Proxy
	if proxy == "" {
		proxy = "—"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s <b>%s</b>\n", statusEmoji(a.Status), esc(a.Label))
	fmt.Fprintf(&b, "Форум: %s\n", forumName(a.Forum))
	fmt.Fprintf(&b, "Статус: %s\n", esc(a.Status))
	fmt.Fprintf(&b, "Тем: %d\n", n)
	fmt.Fprintf(&b, "Прокси: %s\n", esc(proxy))
	if note != "" {
		fmt.Fprintf(&b, "\n%s", note)
	}

	m := &tele.ReplyMarkup{}
	m.Inline(
		m.Row(m.Data("🔄 Проверить", uAccCheck, itoa(id)), m.Data("✏️ Прокси", uAccProxy, itoa(id))),
		m.Row(m.Data("🗑 Удалить", uAccDel, itoa(id))),
		m.Row(m.Data("⬅️ Назад", uAccounts)),
	)
	return b.String(), m, nil
}

func (tb *Bot) accCheck(c tele.Context) error {
	id := firstID(c)
	ctx, cancel := tb.dbCtx()
	a, err := tb.store.GetAccount(ctx, id)
	cancel()
	if err != nil {
		return err
	}
	nctx, ncancel := tb.netCtx()
	defer ncancel()
	note := "🟢 Авторизация успешна."
	if err := tb.sched.CheckAuth(nctx, a); err != nil {
		note = "🔴 Ошибка: " + esc(err.Error())
		if err == forum.ErrAuthFailed {
			note = "🔴 Авторизация не прошла."
		}
	}
	return tb.renderAccount(c, id, note)
}

func (tb *Bot) accDelConfirm(c tele.Context) error {
	id := firstID(c)
	m := &tele.ReplyMarkup{}
	m.Inline(
		m.Row(m.Data("✅ Да, удалить", uAccDelOK, itoa(id)), m.Data("⬅️ Нет", uAccView, itoa(id))),
	)
	return tb.show(c, "Удалить аккаунт вместе со всеми его темами?", m)
}

func (tb *Bot) accDelete(c tele.Context) error {
	ctx, cancel := tb.dbCtx()
	defer cancel()
	if err := tb.store.DeleteAccount(ctx, firstID(c)); err != nil {
		return err
	}
	return tb.showAccounts(c)
}

func (tb *Bot) accProxyPrompt(c tele.Context) error {
	id := firstID(c)
	tb.fsm.set(c.Sender().ID, &state{step: stepEditProxy, accountID: id})
	m := &tele.ReplyMarkup{}
	m.Inline(cancelRow(m, uAccView, itoa(id)))
	return tb.show(c, "Отправьте прокси в формате <code>socks5://user:pass@ip:port</code>.\nОтправьте «-» чтобы убрать прокси.", m)
}

func (tb *Bot) inputEditProxy(c tele.Context, st *state) error {
	proxy := strings.TrimSpace(c.Text())
	if proxy == "-" {
		proxy = ""
	}
	tb.fsm.clear(c.Sender().ID)
	ctx, cancel := tb.dbCtx()
	defer cancel()
	if err := tb.store.SetAccountProxy(ctx, st.accountID, proxy); err != nil {
		return err
	}
	return tb.sendAccountByID(c, st.accountID, "✅ Прокси обновлён.")
}
