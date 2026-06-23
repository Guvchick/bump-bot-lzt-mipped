package tg

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/guvchick/bumpbot/internal/storage"
	tele "gopkg.in/telebot.v4"
)

// settingCodes maps short callback codes to storage keys + labels.
var settingCodes = map[string]struct {
	key   string
	label string
}{
	"di": {storage.KeyDefaultIntervalSec, "Интервал по умолчанию (Mipped), сек"},
	"li": {storage.KeyLolzIntervalSec, "Интервал Lolzteam, сек"},
	"ji": {storage.KeyJitterSec, "Джиттер, сек"},
	"sp": {storage.KeyStatsPollSec, "Период статистики, сек"},
	"rd": {storage.KeyRequestDelayMS, "Задержка между запросами, мс"},
}

func (tb *Bot) showSettings(c tele.Context) error {
	s := tb.sched.CurrentSettings()
	var b strings.Builder
	b.WriteString("⚙️ <b>Настройки</b>\n\n")
	fmt.Fprintf(&b, "Интервал по умолч. (Mipped): <b>%d</b> сек\n", s.DefaultIntervalSec)
	fmt.Fprintf(&b, "Интервал Lolzteam: <b>%d</b> сек\n", s.LolzIntervalSec)
	fmt.Fprintf(&b, "Джиттер: <b>%d</b> сек\n", s.JitterSec)
	fmt.Fprintf(&b, "Период статистики: <b>%d</b> сек\n", s.StatsPollSec)
	fmt.Fprintf(&b, "Задержка запросов: <b>%d</b> мс\n", s.RequestDelayMS)
	fmt.Fprintf(&b, "Уведомления: <b>%s</b>\n", onOff(s.Notifications))

	notifLabel := "🔔 Уведомления: выкл"
	if s.Notifications {
		notifLabel = "🔕 Уведомления: вкл"
	}
	m := &tele.ReplyMarkup{}
	m.Inline(
		m.Row(m.Data("Интервал Mipped", uSetEdit, "di"), m.Data("Интервал Lolz", uSetEdit, "li")),
		m.Row(m.Data("Джиттер", uSetEdit, "ji"), m.Data("Период стат.", uSetEdit, "sp")),
		m.Row(m.Data("Задержка запросов", uSetEdit, "rd")),
		m.Row(m.Data(notifLabel, uNotifTgl)),
		m.Row(m.Data("⬅️ Назад", uMenu)),
	)
	return tb.show(c, b.String(), m)
}

func (tb *Bot) settingEditPrompt(c tele.Context) error {
	code := firstArg(c)
	sc, ok := settingCodes[code]
	if !ok {
		return tb.showSettings(c)
	}
	tb.fsm.set(c.Sender().ID, &state{step: stepEditSetting, settingKey: sc.key})
	m := &tele.ReplyMarkup{}
	m.Inline(cancelRow(m, uSettings))
	return tb.show(c, fmt.Sprintf("Введите новое значение: <b>%s</b>", esc(sc.label)), m)
}

func (tb *Bot) inputEditSetting(c tele.Context, st *state) error {
	n, err := strconv.Atoi(strings.TrimSpace(c.Text()))
	if err != nil || n < 0 {
		return c.Send("Введите неотрицательное число:")
	}
	tb.fsm.clear(c.Sender().ID)
	ctx, cancel := tb.dbCtx()
	defer cancel()
	if err := tb.store.SetSetting(ctx, st.settingKey, strconv.Itoa(n)); err != nil {
		return err
	}
	if err := tb.sched.Reload(ctx); err != nil {
		return err
	}
	return c.Send("✅ Настройка сохранена.", tb.settingsMarkup())
}

func (tb *Bot) settingToggleNotif(c tele.Context) error {
	cur := tb.sched.CurrentSettings().Notifications
	val := "0"
	if !cur {
		val = "1"
	}
	ctx, cancel := tb.dbCtx()
	defer cancel()
	if err := tb.store.SetSetting(ctx, storage.KeyNotifications, val); err != nil {
		return err
	}
	if err := tb.sched.Reload(ctx); err != nil {
		return err
	}
	return tb.showSettings(c)
}

// settingsMarkup is a tiny keyboard to jump back to settings after a text edit.
func (tb *Bot) settingsMarkup() *tele.ReplyMarkup {
	m := &tele.ReplyMarkup{}
	m.Inline(m.Row(m.Data("⚙️ К настройкам", uSettings)), m.Row(m.Data("🏠 Меню", uMenu)))
	return m
}

func onOff(b bool) string {
	if b {
		return "вкл"
	}
	return "выкл"
}
