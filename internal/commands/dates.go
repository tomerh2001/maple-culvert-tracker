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

// messageLinkRe matches a Discord message link
// (https://discord.com/channels/<guild>/<channel>/<message>, also the
// discordapp.com / canary/ptb host variants), capturing channel and message
// ids.
var messageLinkRe = regexp.MustCompile(`^https://(?:[a-z]+\.)?discord(?:app)?\.com/channels/\d+/(\d+)/(\d+)/?$`)

var errBadDate = errors.New("bad date")

var errBadMessageLink = errors.New("bad message link")

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

// parseMessageLink extracts the channel and message ids from a Discord message
// link (right click a message -> Copy Message Link). The guild segment is
// ignored: the bot fetches by channel + message id.
func parseMessageLink(s string) (channelID, messageID string, err error) {
	s = strings.TrimSpace(s)
	m := messageLinkRe.FindStringSubmatch(s)
	if m == nil {
		return "", "", errBadMessageLink
	}
	return m[1], m[2], nil
}

const badMessageLinkMessage = "Invalid `message-link` - right click the screenshot message and choose Copy Message Link (looks like `https://discord.com/channels/…/…/…`)."
