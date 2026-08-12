package commands

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// discordTimestampRe matches a Discord timestamp mention like <t:1723500000>
// or <t:1723500000:R>.
var discordTimestampRe = regexp.MustCompile(`^<t:(\d+)(?::[a-zA-Z])?>$`)

var errBadDate = errors.New("bad date")

// parseFlexibleDate parses a user-typed date option: plain YYYY-MM-DD or a
// Discord timestamp mention (<t:123456> / <t:123456:R>). The result is the
// named calendar date at 00:00 UTC. Explicit dates name a week LABEL, not an
// instant - callers wanting the week key normalize the result with
// helpers.GetCulvertResetDate.
func parseFlexibleDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if m := discordTimestampRe.FindStringSubmatch(s); m != nil {
		secs, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return time.Time{}, errBadDate
		}
		t := time.Unix(secs, 0).UTC()
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), nil
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return time.Time{}, errBadDate
	}
	return t, nil
}

const badDateMessage = "Invalid date - use `YYYY-MM-DD` or a Discord timestamp like `<t:1723500000>`."
