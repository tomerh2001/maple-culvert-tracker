package helpers

import (
	"database/sql"
	"log"

	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	redis "github.com/valkey-io/valkey-go"
)

// RunTenantBackfill stamps every pre-tenant postgres row (guild_id = ”) with
// the primary env guild id, i.e. the default tenant. One-shot via the
// DATA_FIXES_TENANT_BACKFILL marker, and idempotent regardless (the WHERE
// clause matches nothing after the first run). Must run AFTER postgres
// migration 5 added the guild_id columns.
func RunTenantBackfill(db *sql.DB, rdb *redis.Client) error {
	res, err := apiredis.DATA_FIXES_TENANT_BACKFILL.Global().Get(rdb)
	if err != nil && err != redis.Nil {
		log.Println("Failed to get redis val "+apiredis.DATA_FIXES_TENANT_BACKFILL.ToString(), err)
		return err
	}
	if res == "true" {
		log.Println("Already ran TenantBackfill")
		return nil
	}

	primary := data.PrimaryGuildID()
	for _, table := range []string{"characters", "weekly_announcements"} {
		result, err := db.Exec(`UPDATE `+table+` SET guild_id = $1 WHERE guild_id = ''`, primary)
		if err != nil {
			log.Println("TenantBackfill: updating "+table+" failed:", err)
			return err
		}
		if n, err := result.RowsAffected(); err == nil && n > 0 {
			log.Printf("TenantBackfill: stamped %d %s row(s) with tenant %s", n, table, primary)
		}
	}

	if err := apiredis.DATA_FIXES_TENANT_BACKFILL.Global().Set(rdb, "true"); err != nil {
		log.Println("Failed to set redis val "+apiredis.DATA_FIXES_TENANT_BACKFILL.ToString(), err)
		return err
	}
	log.Println("TenantBackfill done")
	return nil
}
