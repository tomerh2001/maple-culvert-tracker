package db

import (
	"database/sql"
	"log"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/lib/pq"

	. "github.com/go-jet/jet/v2/postgres"
	"github.com/tomerh2001/maple-culvert-tracker/.gen/mapleculverttrackerdb/public/model"
	. "github.com/tomerh2001/maple-culvert-tracker/.gen/mapleculverttrackerdb/public/table"
)

func Migrate(db *sql.DB) error {
	log.Println("Creating migration driver")
	driver, err := postgres.WithInstance(db, &postgres.Config{})
	if err != nil {
		return err
	}
	m, err := migrate.NewWithDatabaseInstance(
		"file://./db_migrations",
		"postgres",
		driver,
	)
	if err != nil {
		return err
	}
	log.Println("Migrating...")
	uperr := m.Up()
	if uperr != nil && uperr != migrate.ErrNoChange {
		// attempt recovery force down
		log.Println("Migration failed, recovering...")
		stmt := SELECT(SchemaMigrations.Version, SchemaMigrations.Dirty).FROM(SchemaMigrations).LIMIT(1)
		schema := model.SchemaMigrations{}
		err := stmt.Query(db, &schema)
		if err != nil {
			log.Println("Migration recovery failed to get database migration version")
			return err
		}
		if !schema.Dirty {
			log.Println("Migration recovery not necessary, schema is not dirty")
			return uperr
		}
		forceVersion := schema.Version - 1
		log.Println("Migration recovery will force schema back 1 version, to version", forceVersion)
		err = m.Force(int(forceVersion))
		if err != nil {
			log.Println("Migration recovery failed to force version to", forceVersion)
			return err
		}
		log.Println("Migration recovery forced version to", forceVersion)
		return uperr
	}

	return nil
}
