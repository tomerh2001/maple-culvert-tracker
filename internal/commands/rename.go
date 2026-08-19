package commands

import (
	"log"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/api/helpers"
	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

// charRow is one characters-table row matched by name.
type charRow struct {
	id    int64
	name  string // exact stored spelling/casing
	owner string
}

// charsByNameCI returns every character in the tenant whose name equals `name`
// case-insensitively, real-owner rows first then by id. The name column's
// UNIQUE(guild_id, name) is case-SENSITIVE, so case-variant duplicates ('Bob'
// vs 'BOB') can coexist; returning ALL matches lets /rename detect that and
// refuse rather than act on an arbitrary one.
func charsByNameCI(tenant, name string) ([]charRow, error) {
	rows, err := db.DB.Query(
		`SELECT id, maple_character_name, discord_user_id FROM characters
		 WHERE guild_id = $1 AND LOWER(maple_character_name) = LOWER($2)
		 ORDER BY (discord_user_id NOT IN ('1','2','')) DESC, id`, tenant, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []charRow
	for rows.Next() {
		var c charRow
		if err := rows.Scan(&c.id, &c.name, &c.owner); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func rowNames(rows []charRow) []string {
	out := make([]string, 0, len(rows))
	for _, c := range rows {
		out = append(out, c.name)
	}
	return out
}

// mergedOwner picks the surviving owner when two rows merge: a real Discord id
// wins (from either side); otherwise a tracked-but-unlinked '2' beats an
// untracked '1'/” so merged scores stay visible on the weekly board; only when
// both are untracked does the target keep its own value.
func mergedOwner(tgt, src string) string {
	real := func(o string) bool { return o != "1" && o != "2" && o != "" }
	switch {
	case real(tgt):
		return tgt
	case real(src):
		return src
	case tgt == "2" || src == "2":
		return "2"
	default:
		return tgt
	}
}

// renameCharacter fixes a misread (or in-game-changed) character name. If the
// correct IGN isn't tracked it's a plain rename; if it already exists as a
// distinct character the two are MERGED: the source's scores move onto the
// target (source wins a same-week conflict - it is usually the fresh
// submission), the surviving owner is the more-visible of the two, and the
// source row is deleted - all in one transaction. Submitter/admin gated;
// refuses when a name is case-ambiguous rather than guessing.
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

	srcRows, err := charsByNameCI(tenant, oldName)
	if err != nil {
		log.Println("rename: source lookup:", err)
		r.Edit("Something went wrong. Please try again later.")
		return
	}
	switch len(srcRows) {
	case 0:
		r.Edit("No character named `" + oldName + "` found in this server.")
		return
	case 1:
		// ok
	default:
		r.Edit("`" + oldName + "` matches multiple rows (`" + strings.Join(rowNames(srcRows), "`, `") + "`) that differ only by capitalization. Rename them one at a time by their exact spelling, or ask an admin to sort it out." + warning)
		return
	}
	src := srcRows[0]

	tgtRows, err := charsByNameCI(tenant, newName)
	if err != nil {
		log.Println("rename: target lookup:", err)
		r.Edit("Something went wrong. Please try again later.")
		return
	}
	// Distinct targets: any matching row that ISN'T the source itself.
	var tgts []charRow
	for _, t := range tgtRows {
		if t.id != src.id {
			tgts = append(tgts, t)
		}
	}

	switch {
	case len(tgts) == 0:
		// No other character owns this name: a plain rename (also the path that
		// fixes a case-only correction, where the only match is the source).
		if src.name == newName {
			r.Edit("`" + src.name + "` already resolves to `" + newName + "`. Nothing to do." + warning)
			return
		}
		if _, err := db.DB.Exec(`UPDATE characters SET maple_character_name = $1 WHERE id = $2`, newName, src.id); err != nil {
			log.Println("rename: update failed:", err)
			r.Edit("Something went wrong renaming the character. Please try again later.")
			return
		}
		r.Edit("Renamed `" + src.name + "` to `" + newName + "`." + warning + refreshWeeklyLine(s, i))
		return
	case len(tgts) > 1:
		r.Edit("`" + newName + "` matches multiple rows (`" + strings.Join(rowNames(tgts), "`, `") + "`) that differ only by capitalization - clean those up first so I know which one to merge into." + warning)
		return
	}
	tgt := tgts[0]

	// Merge src -> tgt atomically: move scores (source wins a same-week
	// conflict), promote the surviving owner, delete the source.
	tx, err := db.DB.Begin()
	if err != nil {
		log.Println("rename: begin tx:", err)
		r.Edit("Something went wrong. Please try again later.")
		return
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback()
		}
	}()

	if _, err := tx.Exec(
		// updated_at rides along with the score it belongs to: a rename moves
		// existing rows, it does not change anyone's score.
		`INSERT INTO character_culvert_scores (culvert_date, character_id, score, updated_at)
		 SELECT culvert_date, $1, score, updated_at FROM character_culvert_scores WHERE character_id = $2
		 ON CONFLICT (culvert_date, character_id) DO UPDATE SET score = EXCLUDED.score, updated_at = EXCLUDED.updated_at`,
		tgt.id, src.id); err != nil {
		log.Println("rename: merge scores failed:", err)
		r.Edit("Something went wrong merging scores. Please try again later.")
		return
	}
	if owner := mergedOwner(tgt.owner, src.owner); owner != tgt.owner {
		if _, err := tx.Exec(`UPDATE characters SET discord_user_id = $1 WHERE id = $2`, owner, tgt.id); err != nil {
			log.Println("rename: owner promote failed:", err)
			r.Edit("Something went wrong. Please try again later.")
			return
		}
	}
	if _, err := tx.Exec(`DELETE FROM characters WHERE id = $1`, src.id); err != nil {
		log.Println("rename: delete source failed:", err)
		r.Edit("Something went wrong. Please try again later.")
		return
	}
	if err := tx.Commit(); err != nil {
		log.Println("rename: commit failed:", err)
		r.Edit("Something went wrong. Please try again later.")
		return
	}
	committed = true

	r.Edit("Merged `" + src.name + "` into `" + tgt.name + "` - scores combined, old entry removed." + warning + refreshWeeklyLine(s, i))
}
