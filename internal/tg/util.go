package tg

import (
	"strconv"

	tele "gopkg.in/telebot.v4"
)

// firstID parses the first callback argument as an int64 (0 if absent/invalid).
func firstID(c tele.Context) int64 {
	args := c.Args()
	if len(args) == 0 {
		return 0
	}
	id, _ := strconv.ParseInt(args[0], 10, 64)
	return id
}

// firstArg returns the first callback argument (empty if absent).
func firstArg(c tele.Context) string {
	args := c.Args()
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }
