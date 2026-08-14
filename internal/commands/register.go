package commands

import (
	"database/sql"
	"log"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/api/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

// characterOwnedByAnother reports whether an existing character row is linked to
// a DIFFERENT real member than the intended target: its owner is neither the
// target nor a sentinel (untracked '1', unlinked '2', or the empty pre-tenant
// default). /register refuses to move such a character unless the caller can
// submit scores. Factored out so the multi-IGN ownership invariant is testable.
func characterOwnedByAnother(existingOwner, targetID string) bool {
	return existingOwner != targetID &&
		existingOwner != "1" && existingOwner != "2" && existingOwner != ""
}

// registerOutcome is the result of linking one IGN (see registerIGN). It lets
// both the single-name path and the nickname-parsing path share one code path
// while formatting their own replies.
type registerOutcome int

const (
	regRegistered     registerOutcome = iota // newly inserted or (re)linked
	regAlreadyYours                          // already linked to the target (no-op)
	regOwnedByAnother                        // owned by a different real member, refused
	regError                                 // a database error occurred
)

// registerResult carries the canonical name plus the outcome of one IGN.
type registerResult struct {
	name     string          // canonical spelling (rankings) or the input if unverified
	outcome  registerOutcome // what happened
	owner    string          // existing owner id, only set for regOwnedByAnother
	verified bool            // true when the rankings confirmed the spelling
}

// registerIGN canonicalises one IGN against the official rankings (ADVISORY: a
// lookup failure or miss never blocks registration) and then inserts or
// (re)links its character row for targetID within the tenant, applying the
// ownership gate. It performs NO Discord reply, so the single explicit-name
// path and the nickname path share exactly this logic. canSubmit lets a
// submitter/admin move a name owned by another real member.
func registerIGN(tenant, name, targetID string, canSubmit bool) registerResult {
	res := registerResult{name: name, verified: true}
	charData, err := helpers.FetchCharacterData(name, apiredis.OPTIONAL_CONF_MAPLE_REGION.For(tenant).GetWithDefault(apiredis.RedisDB, "na"))
	if err != nil || charData == nil {
		res.verified = false
	} else {
		res.name = charData.CharacterName
	}

	var existingID int64
	var existingOwner string
	err = db.DB.QueryRow(
		`SELECT id, discord_user_id FROM characters WHERE LOWER(maple_character_name) = LOWER($1) AND guild_id = $2`,
		res.name, tenant).Scan(&existingID, &existingOwner)
	switch {
	case err == sql.ErrNoRows:
		if _, err := db.DB.Exec(
			`INSERT INTO characters (maple_character_name, discord_user_id, guild_id) VALUES ($1, $2, $3)`,
			res.name, targetID, tenant); err != nil {
			log.Println("register: insert failed:", err)
			res.outcome = regError
			return res
		}
		res.outcome = regRegistered
	case err != nil:
		log.Println("register: lookup failed:", err)
		res.outcome = regError
	case existingOwner == targetID:
		res.outcome = regAlreadyYours
	case characterOwnedByAnother(existingOwner, targetID) && !canSubmit:
		res.outcome = regOwnedByAnother
		res.owner = existingOwner
	default:
		// Unlinked, or a submitter/admin relinking it to the target.
		if _, err := db.DB.Exec(
			`UPDATE characters SET discord_user_id = $1 WHERE id = $2`,
			targetID, existingID); err != nil {
			log.Println("register: update failed:", err)
			res.outcome = regError
			return res
		}
		res.outcome = regRegistered
	}
	return res
}

// registerCharacter links a MapleStory character to a Discord account:
// your own by default, or someone else's via user:@member (submitters only).
// With no name: it infers the caller's IGNs from their server nickname (see
// registerFromNickname).
func registerCharacter(s *discordgo.Session, i *discordgo.InteractionCreate) {
	r := deferReply(s, i, true)
	tenant := tenantOf(i)

	characterName := ""
	callerID := i.Member.User.ID
	targetID := callerID
	userGiven := false
	for _, v := range i.ApplicationCommandData().Options {
		switch v.Name {
		case "name":
			characterName = strings.TrimSpace(v.StringValue())
		case "user":
			if u := v.UserValue(nil); u != nil {
				targetID = u.ID
				userGiven = true
			}
		}
	}

	// No explicit name -> infer the caller's IGNs from their server nickname.
	if characterName == "" {
		registerFromNickname(s, i, r, tenant, callerID, userGiven)
		return
	}

	canSubmit := canSubmitScores(i)
	forOther := targetID != callerID
	if forOther && !canSubmit {
		r.Edit("Registering a character for someone else needs submitter permissions. They can register themselves with `/register name:TheirCharacter`.")
		return
	}

	who := "you"
	if forOther {
		who = "<@" + targetID + ">"
	}

	res := registerIGN(tenant, characterName, targetID, canSubmit)
	// The rankings check is ADVISORY: an unverified name still registers, it
	// only adds a warning to the receipt.
	warning := ""
	if !res.verified {
		warning = "\n:warning: I couldn't verify `" + res.name + "` against the official rankings - double-check the spelling (`/unregister` + `/register` fixes typos)."
	}
	switch res.outcome {
	case regError:
		r.Edit("Something went wrong saving the character. Please try again later.")
		return
	case regAlreadyYours:
		r.Edit("`" + res.name + "` is already registered to " + who + ". Nothing to do!" + warning)
		return
	case regOwnedByAnother:
		r.Edit("`" + res.name + "` is already registered to <@" + res.owner + ">. If that's wrong, ask an admin or submitter to move it with `/register name:" + res.name + " user:@the-right-person`.")
		return
	}

	warning += refreshWeeklyLine(s, i)
	if forOther {
		r.Edit("Done! `" + res.name + "` is now registered to " + who + ". :tada:" + warning)
		return
	}
	r.Edit("Done! `" + res.name + "` is now registered to you. :tada:" + warning + "\nTry `/culvert` to see your progression once your scores are in.")
}

// registerFromNickname handles `/register` with no name: it parses the caller's
// server nickname into IGNs (see parseNicknameIGNs) and registers each for the
// CALLER only, replying with one combined ephemeral receipt. The user: option
// is intentionally ignored here - a nickname only ever describes its own owner
// - so we require an explicit name: when user: is set rather than parse a
// nickname on someone else's behalf.
func registerFromNickname(s *discordgo.Session, i *discordgo.InteractionCreate, r *reply, tenant, callerID string, userGiven bool) {
	if userGiven {
		r.Edit("To register for someone else, include the character name: `/register name:TheirCharacter user:@member`. Without a name I can only read *your* nickname.")
		return
	}

	source := i.Member.Nick
	if source == "" {
		source = i.Member.User.GlobalName
	}
	if source == "" {
		source = i.Member.User.Username
	}
	igns := parseNicknameIGNs(source)
	if len(igns) == 0 {
		r.Edit("I couldn't find any character names in your server nickname. Register explicitly with `/register name:YourCharacter`.")
		return
	}

	canSubmit := canSubmitScores(i)
	seen := map[string]bool{}
	var registered, alreadyYours, ownedByOther, failed, unverified []string
	for _, ign := range igns {
		if key := strings.ToLower(ign); seen[key] {
			continue // ignore a name repeated within the nickname
		} else {
			seen[key] = true
		}
		res := registerIGN(tenant, ign, callerID, canSubmit)
		switch res.outcome {
		case regRegistered:
			registered = append(registered, res.name)
		case regAlreadyYours:
			alreadyYours = append(alreadyYours, res.name)
		case regOwnedByAnother:
			ownedByOther = append(ownedByOther, res.name)
		case regError:
			failed = append(failed, res.name)
		}
		// Flag unverified spelling only where it registered or was already
		// yours - it's noise on a name owned by someone else or a DB failure.
		if !res.verified && (res.outcome == regRegistered || res.outcome == regAlreadyYours) {
			unverified = append(unverified, res.name)
		}
	}

	parts := []string{}
	if len(registered) > 0 {
		parts = append(parts, "Registered: "+quoteList(registered))
	}
	if len(alreadyYours) > 0 {
		parts = append(parts, "already yours: "+quoteList(alreadyYours))
	}
	if len(ownedByOther) > 0 {
		parts = append(parts, "skipped (owned by someone else): "+quoteList(ownedByOther))
	}
	if len(failed) > 0 {
		parts = append(parts, "couldn't save: "+quoteList(failed))
	}
	receipt := strings.Join(parts, "; ")
	if receipt != "" { // capitalise the leading label for a standalone sentence
		receipt = strings.ToUpper(receipt[:1]) + receipt[1:]
	}
	if len(unverified) > 0 {
		receipt += "\n:warning: Couldn't verify " + quoteList(unverified) + " against the official rankings - double-check the spelling."
	}
	receipt += refreshWeeklyLine(s, i)
	r.Edit(receipt)
}

// quoteList renders names as a comma-separated backtick-quoted list.
func quoteList(names []string) string {
	return "`" + strings.Join(names, "`, `") + "`"
}

// unregisterCharacter untracks a character by name, or ALL characters
// registered to a member (user option / the invoker by default). History is
// always kept (rows keep their scores; discord_user_id becomes '1').
func unregisterCharacter(s *discordgo.Session, i *discordgo.InteractionCreate) {
	r := deferReply(s, i, true)
	tenant := tenantOf(i)

	name := ""
	callerID := i.Member.User.ID
	targetID := callerID
	userGiven := false
	for _, v := range i.ApplicationCommandData().Options {
		switch v.Name {
		case "name":
			name = strings.TrimSpace(v.StringValue())
		case "user":
			if u := v.UserValue(nil); u != nil {
				targetID = u.ID
				userGiven = true
			}
		}
	}

	// name given -> untrack that one character.
	if name != "" {
		var charID int64
		var owner, realName string
		err := db.DB.QueryRow(
			`SELECT id, discord_user_id, maple_character_name FROM characters WHERE LOWER(maple_character_name) = LOWER($1) AND discord_user_id != '1' AND guild_id = $2`,
			name, tenant).Scan(&charID, &owner, &realName)
		if err == sql.ErrNoRows {
			r.Edit("No tracked character named `" + name + "` found - nothing to do.")
			return
		}
		if err != nil {
			log.Println("unregister: lookup failed:", err)
			r.Edit("Something went wrong. Please try again later.")
			return
		}
		if owner != callerID && !canSubmitScores(i) {
			r.Edit("`" + realName + "` isn't registered to you - untracking someone else's character needs submitter permissions.")
			return
		}
		if _, err := db.DB.Exec(`UPDATE characters SET discord_user_id = '2' WHERE id = $1`, charID); err != nil {
			log.Println("unregister: update failed:", err)
			r.Edit("Something went wrong. Please try again later.")
			return
		}
		r.Edit("`" + realName + "` is no longer linked to your Discord account. Its scores stay on the board - `/register` links it back anytime." + refreshWeeklyLine(s, i))
		return
	}

	// No name -> untrack ALL characters registered to the target member.
	if userGiven && targetID != callerID && !canSubmitScores(i) {
		r.Edit("Unregistering someone else's characters needs submitter permissions.")
		return
	}
	rows, err := db.DB.Query(
		`SELECT maple_character_name FROM characters WHERE discord_user_id = $1 AND guild_id = $2 ORDER BY maple_character_name`, targetID, tenant)
	if err != nil {
		log.Println("unregister: list failed:", err)
		r.Edit("Something went wrong. Please try again later.")
		return
	}
	names := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err == nil {
			names = append(names, n)
		}
	}
	rows.Close()

	who := "you"
	if targetID != callerID {
		who = "<@" + targetID + ">"
	}
	if len(names) == 0 {
		r.Edit("No characters are registered to " + who + " - nothing to do.")
		return
	}
	if _, err := db.DB.Exec(`UPDATE characters SET discord_user_id = '2' WHERE discord_user_id = $1 AND guild_id = $2`, targetID, tenant); err != nil {
		log.Println("unregister: bulk update failed:", err)
		r.Edit("Something went wrong. Please try again later.")
		return
	}
	r.Edit("Unlinked `" + strings.Join(capList(names, 25), "`, `") + "` from " + who + ". Their scores stay on the board; `/register` re-links anytime." + refreshWeeklyLine(s, i))
}
