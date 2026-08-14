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

// renameCharacter fixes an OCR misread (or an in-game name change) by renaming
// a character to the correct IGN. If the correct name is ALREADY a distinct
// tracked character, the two are MERGED: the misread stub's scores move onto
// the real character (the stub wins a week conflict - it is usually the fresh
// submission), the owner link is preserved, and the stub row is deleted.
// Submitter/admin gated; refreshes the weekly message afterwards.
func renameCharacter(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if !requireSubmitPermission(s, i) {
		return
	}
	r := deferReply(s, i, true)
	tenant := tenantOf(i)

	oldName, newName := "", ""
	for _, v := range i.ApplicationCommandData().Options {
		switch v.Name {
		case "name":
			oldName = strings.TrimSpace(v.StringValue())
		case "new-name":
			newName = strings.TrimSpace(v.StringValue())
		}
	}
	if oldName == "" || newName == "" {
		r.Edit("Usage: `/rename name:<misread> new-name:<correct>`.")
		return
	}

	// Canonicalize the target against the official rankings (advisory only).
	warning := ""
	if cd, err := helpers.FetchCharacterData(newName, apiredis.OPTIONAL_CONF_MAPLE_REGION.For(tenant).GetWithDefault(apiredis.RedisDB, "na")); err == nil && cd != nil {
		newName = cd.CharacterName
	} else {
		warning = "\n:warning: Couldn't verify `" + newName + "` against the official rankings - double-check the spelling."
	}

	var srcID int64
	var srcOwner string
	err := db.DB.QueryRow(
		`SELECT id, discord_user_id FROM characters WHERE guild_id = $1 AND LOWER(maple_character_name) = LOWER($2)`,
		tenant, oldName).Scan(&srcID, &srcOwner)
	if err == sql.ErrNoRows {
		r.Edit("No character named `" + oldName + "` found in this server.")
		return
	}
	if err != nil {
		log.Println("rename: source lookup:", err)
		r.Edit("Something went wrong. Please try again later.")
		return
	}

	var tgtID int64
	var tgtOwner string
	terr := db.DB.QueryRow(
		`SELECT id, discord_user_id FROM characters WHERE guild_id = $1 AND LOWER(maple_character_name) = LOWER($2)`,
		tenant, newName).Scan(&tgtID, &tgtOwner)
	switch {
	case terr == sql.ErrNoRows:
		// No existing target: a plain rename.
		if _, err := db.DB.Exec(`UPDATE characters SET maple_character_name = $1 WHERE id = $2`, newName, srcID); err != nil {
			log.Println("rename: update failed:", err)
			r.Edit("Something went wrong renaming the character. Please try again later.")
			return
		}
		r.Edit("Renamed `" + oldName + "` to `" + newName + "`." + warning + refreshWeeklyLine(s, i))
		return
	case terr != nil:
		log.Println("rename: target lookup:", terr)
		r.Edit("Something went wrong. Please try again later.")
		return
	case tgtID == srcID:
		r.Edit("`" + oldName + "` already resolves to `" + newName + "`. Nothing to do." + warning)
		return
	}

	// Merge src -> tgt: move src's scores onto tgt (stub wins a week conflict),
	// keep the owner link if tgt was unlinked, then delete the stub.
	if _, err := db.DB.Exec(
		`INSERT INTO character_culvert_scores (culvert_date, character_id, score)
		 SELECT culvert_date, $1, score FROM character_culvert_scores WHERE character_id = $2
		 ON CONFLICT (culvert_date, character_id) DO UPDATE SET score = EXCLUDED.score`,
		tgtID, srcID); err != nil {
		log.Println("rename: merge scores failed:", err)
		r.Edit("Something went wrong merging scores. Please try again later.")
		return
	}
	if (tgtOwner == "1" || tgtOwner == "2" || tgtOwner == "") && srcOwner != "1" && srcOwner != "2" && srcOwner != "" {
		if _, err := db.DB.Exec(`UPDATE characters SET discord_user_id = $1 WHERE id = $2`, srcOwner, tgtID); err != nil {
			log.Println("rename: transfer link failed:", err)
		}
	}
	if _, err := db.DB.Exec(`DELETE FROM characters WHERE id = $1`, srcID); err != nil {
		log.Println("rename: delete source failed:", err)
		r.Edit("Merged scores but couldn't remove the old `" + oldName + "` row. Please try again later.")
		return
	}
	r.Edit("Merged `" + oldName + "` into `" + newName + "` - scores combined, old entry removed." + warning + refreshWeeklyLine(s, i))
}
