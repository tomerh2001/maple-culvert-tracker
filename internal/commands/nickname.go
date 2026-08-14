package commands

import "strings"

// maxNicknameIGNs caps how many in-game names a single server nickname may
// register in one `/register` call, so a pathological nickname can't spam the
// roster.
const maxNicknameIGNs = 10

// parseNicknameIGNs extracts the in-game names encoded in a Discord server
// nickname. The house format is "<guild tag> | IGN1/IGN2/IGN3": everything
// AFTER the last '|' is the IGN list (no '|' -> the whole nickname is the
// list). That list is split on '/' or ',', each token trimmed, empties
// dropped, and the result capped at maxNicknameIGNs. It is pure so the parse
// is unit-tested without a Discord session.
//
// Examples:
//
//	"Sen | Senpapi/Pichi/DegenDaddy" -> [Senpapi Pichi DegenDaddy]
//	"Nunez"                          -> [Nunez]
//	"Tag | A / B , C"                -> [A B C]
//	"" or "Tag |"                    -> []
func parseNicknameIGNs(nick string) []string {
	if idx := strings.LastIndex(nick, "|"); idx >= 0 {
		nick = nick[idx+1:]
	}
	fields := strings.FieldsFunc(nick, func(r rune) bool {
		return r == '/' || r == ','
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if t := strings.TrimSpace(f); t != "" {
			out = append(out, t)
			if len(out) >= maxNicknameIGNs {
				break
			}
		}
	}
	return out
}
