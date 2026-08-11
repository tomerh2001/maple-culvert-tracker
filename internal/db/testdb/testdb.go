// Package testdb provides a real-postgres harness for integration tests.
//
// Tests opt in by reading TEST_DATABASE_URL (e.g.
// postgres://postgres:postgres@localhost:5432/test?sslmode=disable); when it
// is unset the test is skipped, so the ordinary `go test ./...` run stays
// green without any infrastructure.
package testdb

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	_ "github.com/lib/pq"
)

// truncateSQL resets every table integration tests write to. RESTART IDENTITY
// keeps generated ids deterministic across tests; CASCADE covers the
// character_culvert_scores -> characters FK.
const truncateSQL = `TRUNCATE characters, character_culvert_scores, weekly_announcements RESTART IDENTITY CASCADE`

// TestDB returns a connection to the database named by TEST_DATABASE_URL,
// skipping the test when it is unset. The schema is (re-)applied from
// db_migrations/*.up.sql - they are idempotent plain SQL, executed directly -
// and the data tables are truncated both now and again at test cleanup, so
// every test starts from an empty, fully migrated database.
func TestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set - skipping database integration test")
	}
	dbc, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("opening TEST_DATABASE_URL: %v", err)
	}
	if err := dbc.Ping(); err != nil {
		t.Fatalf("pinging TEST_DATABASE_URL: %v", err)
	}
	for _, path := range migrationFiles(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading migration %s: %v", filepath.Base(path), err)
		}
		if _, err := dbc.Exec(string(raw)); err != nil {
			t.Fatalf("applying migration %s: %v", filepath.Base(path), err)
		}
	}
	if _, err := dbc.Exec(truncateSQL); err != nil {
		t.Fatalf("truncating test tables: %v", err)
	}
	t.Cleanup(func() {
		if _, err := dbc.Exec(truncateSQL); err != nil {
			t.Errorf("cleanup: truncating test tables: %v", err)
		}
		dbc.Close()
	})
	return dbc
}

// migrationFiles returns the repo's db_migrations/*.up.sql paths ordered by
// their numeric prefix, located relative to this source file.
func migrationFiles(t *testing.T) []string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed - cannot locate db_migrations")
	}
	dir := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "db_migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading migrations dir %s: %v", dir, err)
	}
	files := []string{}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".up.sql") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	if len(files) == 0 {
		t.Fatalf("no *.up.sql migrations found in %s", dir)
	}
	sort.Slice(files, func(a, b int) bool {
		return migrationNumber(files[a]) < migrationNumber(files[b])
	})
	return files
}

// migrationNumber extracts the leading integer of a migration filename
// ("4_weekly_announcements.up.sql" -> 4).
func migrationNumber(path string) int {
	n, _ := strconv.Atoi(strings.SplitN(filepath.Base(path), "_", 2)[0])
	return n
}
