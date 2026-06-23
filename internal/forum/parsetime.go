package forum

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// numUnitRe captures "<number> <word>" pairs, e.g. "23 часов", "57 минут", "38 sec".
var numUnitRe = regexp.MustCompile(`(?i)(\d+)\s*([a-zA-Zа-яёА-ЯЁ]+)`)

// ParseWaitDuration extracts a wait duration from a forum "too early" message,
// supporting both Russian and English unit words in any combination, e.g.:
//
//	"Нужно подождать 0 дней, 23 часов, 57 минут и 38 секунд чтобы поднять тему"
//	"You must wait 4 hours and 12 minutes"
//
// It returns ok=true when at least one number/unit pair was recognised.
func ParseWaitDuration(text string) (time.Duration, bool) {
	matches := numUnitRe.FindAllStringSubmatch(text, -1)
	var total time.Duration
	var found bool
	for _, m := range matches {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		unit := strings.ToLower(m[2])
		switch {
		case strings.HasPrefix(unit, "дн"), strings.HasPrefix(unit, "ден"),
			strings.HasPrefix(unit, "сут"), strings.HasPrefix(unit, "day"):
			total += time.Duration(n) * 24 * time.Hour
			found = true
		case strings.HasPrefix(unit, "час"), strings.HasPrefix(unit, "hour"), strings.HasPrefix(unit, "hr"):
			total += time.Duration(n) * time.Hour
			found = true
		case strings.HasPrefix(unit, "мин"), strings.HasPrefix(unit, "min"):
			total += time.Duration(n) * time.Minute
			found = true
		case strings.HasPrefix(unit, "сек"), strings.HasPrefix(unit, "sec"):
			total += time.Duration(n) * time.Second
			found = true
		}
	}
	return total, found
}
