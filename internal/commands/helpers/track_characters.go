package helpers

import (
	"database/sql"
	"fmt"
	"strings"
)

// SplitNameList splits a comma/newline separated character name list, trimming
// whitespace and dropping empties and duplicates (case-insensitive, first
// occurrence wins, order preserved).
func SplitNameList(raw string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	}) {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, name)
	}
	return out
}

// TrackCharacters inserts the given names as unlinked-but-tracked characters
// (discord_user_id = '2'), skipping names that already exist (ON CONFLICT DO
// NOTHING). It returns the names actually created (in input order) and a map
// of name -> character id covering every input name that exists afterwards.
func TrackCharacters(dbc *sql.DB, names []string) (created []string, ids map[string]int64, err error) {
	created = []string{}
	ids = make(map[string]int64, len(names))
	for _, name := range names {
		var id int64
		err := dbc.QueryRow(
			`INSERT INTO characters (maple_character_name, discord_user_id) VALUES ($1, '2')
			 ON CONFLICT (maple_character_name) DO NOTHING RETURNING id`, name).Scan(&id)
		switch {
		case err == sql.ErrNoRows:
			// Already exists: resolve its id (case-insensitive to also catch
			// case-variant duplicates guarded elsewhere).
			if lerr := dbc.QueryRow(
				`SELECT id FROM characters WHERE LOWER(maple_character_name) = LOWER($1)`, name).Scan(&id); lerr != nil {
				return created, ids, fmt.Errorf("looking up existing character %q: %w", name, lerr)
			}
			ids[name] = id
		case err != nil:
			return created, ids, fmt.Errorf("tracking character %q: %w", name, err)
		default:
			created = append(created, name)
			ids[name] = id
		}
	}
	return created, ids, nil
}
