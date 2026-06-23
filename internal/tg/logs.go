package tg

import (
	"fmt"
	"strings"

	"github.com/guvchick/bumpbot/internal/storage"
	tele "gopkg.in/telebot.v4"
)

func (tb *Bot) showLogs(c tele.Context) error {
	ctx, cancel := tb.dbCtx()
	defer cancel()
	logs, err := tb.store.ListBumpLogs(ctx, 0, 15)
	if err != nil {
		return err
	}

	// Resolve thread names once.
	names := make(map[int64]string)
	threads, _ := tb.store.ListThreads(ctx)
	for _, t := range threads {
		names[t.ID] = threadName(&t)
	}

	var b strings.Builder
	b.WriteString("🧾 <b>Логи апов</b> (последние 15)\n\n")
	if len(logs) == 0 {
		b.WriteString("Пока пусто.")
	}
	for _, l := range logs {
		icon := "❌"
		if l.OK {
			icon = "✅"
		}
		name := names[l.ThreadID]
		if name == "" {
			name = fmt.Sprintf("#%d", l.ThreadID)
		}
		fmt.Fprintf(&b, "%s <b>%s</b> · %s\n", icon, esc(trim(name, 24)), l.At.Local().Format("02.01 15:04"))
		if l.Message != "" {
			fmt.Fprintf(&b, "   %s\n", esc(trim(l.Message, 90)))
		}
		if l.NextAt != nil {
			fmt.Fprintf(&b, "   ⏭ след.: %s\n", relativeLog(l))
		}
	}

	m := &tele.ReplyMarkup{}
	m.Inline(m.Row(m.Data("🔄 Обновить", uLogs)), m.Row(m.Data("⬅️ Назад", uMenu)))
	return tb.show(c, b.String(), m)
}

func relativeLog(l storage.BumpLog) string {
	if l.NextAt == nil {
		return "—"
	}
	return l.NextAt.Local().Format("02.01 15:04")
}
