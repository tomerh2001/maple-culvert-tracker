package helpers

import (
	"context"
	"log"

	"github.com/tomerh2001/maple-culvert-tracker/internal/apiredis"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db"
)

func PreflightTest() {
	log.Println("Starting Main Process")
	log.Println("Testing redis")
	// Test Connection Redis first
	if err := (*apiredis.RedisDB).Do(context.Background(), ((*apiredis.RedisDB).B().Ping().Build())).Error(); err != nil {
		log.Println("Failed to Ping redis")
		log.Fatal(err)
	}
	log.Println("Redis successfully connected")

	// Test Connection Postgres Database
	log.Println("Testing postgres")
	if err := db.DB.Ping(); err != nil {
		log.Println("Failed to Ping postgres")
		log.Fatal(err)
	}
	log.Println("Postgres successfully connected")

	// Apply redis migrations
	log.Println("Applying redis migrations")
	err := apiredis.Migrate(apiredis.RedisDB)
	if err != nil {
		log.Println("Failed to apply redis migrations")
		log.Fatal(err)
	}
	log.Println("Redis migrations successfully applied")

	// Apply postgres migrations
	log.Println("Applying postgres migrations")
	err = db.Migrate(db.DB)
	if err != nil {
		log.Println("Failed to apply postgres migrations")
		log.Fatal(err)
	}

	// None for now
	log.Println("Postgres migrations successfully applied")

	// Apply additional fixes
	log.Println("Applying additional fixes")
	err = RunTenantBackfill(db.DB, apiredis.RedisDB)
	if err != nil {
		log.Println("Failed to RunTenantBackfill")
		log.Fatal(err)
	}
	err = RunSunToWedFixes(db.DB, apiredis.RedisDB)
	if err != nil {
		log.Println("Failed to RunSunToWedFixes")
		log.Fatal(err)
	}
	log.Println("Additional fixes successfully applied")

}
