package db

import (
	"database/sql"
	"net/url"
	"os"

	_ "github.com/lib/pq"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
)

var DB *sql.DB

func init() {
	var err error
	var connStr = "postgresql://" + os.Getenv(data.EnvVarPostgresUser) + ":" + url.QueryEscape(os.Getenv(data.EnvVarPostgresPassword)) + "@" + os.Getenv(data.EnvVarClientPostgresHost) + ":" + os.Getenv(data.EnvVarClientPostgresPort) + "/" + os.Getenv(data.EnvVarPostgresDB) + "?sslmode=disable"
	DB, err = sql.Open("postgres", connStr)
	if err != nil {
		panic(err)
	}
}
