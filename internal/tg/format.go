package tg

import (
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/guvchick/bumpbot/internal/storage"
)

// esc HTML-escapes dynamic text for ModeHTML messages.
func esc(s string) string { return html.EscapeString(s) }

// statusEmoji maps an account status to an indicator.
func statusEmoji(status string) string {
	switch status {
	case storage.StatusOK:
		return "🟢"
	case storage.StatusAuthFailed:
		return "🔴"
	default:
		return "⚪"
	}
}

func forumName(forum string) string {
	switch forum {
	case storage.ForumLolz:
		return "Lolzteam"
	case storage.ForumMipped:
		return "Mipped"
	default:
		return forum
	}
}

func enabledIcon(enabled bool) string {
	if enabled {
		return "✅"
	}
	return "⏸"
}

// humanDuration renders a positive duration with up to two leading units,
// in short Russian form: "2 д 4 ч", "3 ч 12 м", "45 с".
func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return "0 с"
	}
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	hours := d / time.Hour
	d -= hours * time.Hour
	mins := d / time.Minute
	d -= mins * time.Minute
	secs := d / time.Second

	type part struct {
		n    int64
		unit string
	}
	all := []part{{int64(days), "д"}, {int64(hours), "ч"}, {int64(mins), "м"}, {int64(secs), "с"}}
	var out []string
	for _, p := range all {
		if p.n > 0 || len(out) > 0 {
			out = append(out, fmt.Sprintf("%d %s", p.n, p.unit))
		}
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "0 с"
	}
	return strings.Join(out, " ")
}

// nextBumpLabel describes when a thread will next be bumped.
func nextBumpLabel(t *storage.Thread) string {
	if !t.Enabled {
		return "автоап выкл"
	}
	if t.NextBumpAt == nil {
		return "скоро"
	}
	now := time.Now()
	if t.NextBumpAt.After(now) {
		return "через " + humanDuration(t.NextBumpAt.Sub(now))
	}
	return "сейчас"
}

// agoLabel renders "<dur> назад" or a fallback for a past timestamp.
func agoLabel(t *time.Time, none string) string {
	if t == nil {
		return none
	}
	return humanDuration(time.Since(*t)) + " назад"
}

// threadName returns a display name for a thread (title or ref).
func threadName(t *storage.Thread) string {
	if t.Title != "" {
		return t.Title
	}
	return t.ThreadRef
}

// trim shortens a string for inline button labels / log lines.
func trim(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// sparkline renders a tiny bar chart from integer values.
func sparkline(vals []int) string {
	if len(vals) == 0 {
		return "—"
	}
	const blocks = "▁▂▃▄▅▆▇█"
	min, max := vals[0], vals[0]
	for _, v := range vals {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	var b strings.Builder
	span := max - min
	for _, v := range vals {
		idx := 0
		if span > 0 {
			idx = (v - min) * (len(blocks) - 1) / span
		}
		b.WriteString(string([]rune(blocks)[idx]))
	}
	return b.String()
}

// intOrDash renders a *int or "—".
func intOrDash(p *int) string {
	if p == nil {
		return "—"
	}
	return fmt.Sprintf("%d", *p)
}

// delta renders a signed integer delta or "—" when either side is unknown.
func delta(now, then *int) string {
	if now == nil || then == nil {
		return "—"
	}
	d := *now - *then
	if d > 0 {
		return fmt.Sprintf("+%d", d)
	}
	return fmt.Sprintf("%d", d)
}
