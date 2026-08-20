package helpers

// Integration tests for the screenshot archive's postgres layer against a
// real database (see internal/db/testdb). The Discord half (message edits)
// needs a live session and is exercised in production paths only; everything
// the DB layer decides - record lookup, page replace vs insert, deletes,
// guild scoping - is covered here.

import (
	"testing"
	"time"

	"github.com/tomerh2001/maple-culvert-tracker/internal/db/testdb"
)

const (
	arcGuildA = "333333333333333333"
	arcGuildB = "444444444444444444"
)

const arcWeek = "2026-07-29"

func TestScreenshotArchiveLoadMissing(t *testing.T) {
	dbc := testdb.TestDB(t)
	channelID, messageID, pages, err := loadGuildScreenshotArchive(dbc, arcGuildA, arcWeek)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if channelID != "" || messageID != "" || len(pages) != 0 {
		t.Fatalf("missing archive loaded as %q/%q/%v, want empty", channelID, messageID, pages)
	}
	if archiveRowExists(dbc, arcGuildA, arcWeek) {
		t.Fatal("archiveRowExists reported a record that does not exist")
	}
}

func TestScreenshotArchivePageLifecycle(t *testing.T) {
	dbc := testdb.TestDB(t)
	if _, err := dbc.Exec(
		`INSERT INTO weekly_screenshot_archives (guild_id, culvert_date, channel_id, message_id) VALUES ($1, $2, 'chan', 'msg')`,
		arcGuildA, arcWeek); err != nil {
		t.Fatalf("insert record: %v", err)
	}
	if !archiveRowExists(dbc, arcGuildA, arcWeek) {
		t.Fatal("archiveRowExists missed the inserted record")
	}

	// Insert two pages (id 0 = new row).
	if err := saveArchivePage(dbc, arcGuildA, arcWeek, 0, "att-1", []string{"Alpha", "Beta"}); err != nil {
		t.Fatalf("insert page 1: %v", err)
	}
	if err := saveArchivePage(dbc, arcGuildA, arcWeek, 0, "att-2", []string{"Gamma"}); err != nil {
		t.Fatalf("insert page 2: %v", err)
	}
	_, _, pages, err := loadGuildScreenshotArchive(dbc, arcGuildA, arcWeek)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("loaded %d pages, want 2", len(pages))
	}
	if pages[0].attachmentID != "att-1" || len(pages[0].names) != 2 || pages[0].names[0] != "alpha" {
		t.Fatalf("page 1 loaded as %+v", pages[0])
	}

	// Replace page 1: same row, new attachment + names, fresher updated_at.
	firstUpdated := pages[0].updatedAt
	if err := saveArchivePage(dbc, arcGuildA, arcWeek, pages[0].id, "att-1b", []string{"Alpha", "Delta"}); err != nil {
		t.Fatalf("replace page 1: %v", err)
	}
	_, _, pages, err = loadGuildScreenshotArchive(dbc, arcGuildA, arcWeek)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("replace changed the page count: %d", len(pages))
	}
	// Ordering is oldest-updated first, so the replaced page now loads last.
	replaced := pages[1]
	if replaced.attachmentID != "att-1b" || len(replaced.names) != 2 || replaced.names[1] != "delta" {
		t.Fatalf("replaced page loaded as %+v", replaced)
	}
	if !replaced.updatedAt.After(firstUpdated) {
		t.Fatalf("replace did not advance updated_at: %v -> %v", firstUpdated, replaced.updatedAt)
	}

	// Drop one page by id (the attachment cap path).
	if err := deleteArchivePageByID(dbc, pages[0].id); err != nil {
		t.Fatalf("delete page: %v", err)
	}
	if _, _, pages, _ = loadGuildScreenshotArchive(dbc, arcGuildA, arcWeek); len(pages) != 1 {
		t.Fatalf("after delete, %d pages remain, want 1", len(pages))
	}

	// Full teardown (unreachable message): record and pages both go.
	if err := deleteGuildScreenshotArchive(dbc, arcGuildA, arcWeek); err != nil {
		t.Fatalf("delete archive: %v", err)
	}
	if archiveRowExists(dbc, arcGuildA, arcWeek) {
		t.Fatal("record survived deleteGuildScreenshotArchive")
	}
	if _, _, pages, _ = loadGuildScreenshotArchive(dbc, arcGuildA, arcWeek); len(pages) != 0 {
		t.Fatalf("pages survived deleteGuildScreenshotArchive: %v", pages)
	}
}

// Archives are keyed per guild: one guild's pages and clears never touch
// another's.
func TestScreenshotArchiveGuildScoped(t *testing.T) {
	dbc := testdb.TestDB(t)
	for _, gid := range []string{arcGuildA, arcGuildB} {
		if _, err := dbc.Exec(
			`INSERT INTO weekly_screenshot_archives (guild_id, culvert_date, channel_id, message_id) VALUES ($1, $2, 'chan', 'msg-'||$1)`,
			gid, arcWeek); err != nil {
			t.Fatalf("insert record for %s: %v", gid, err)
		}
		if err := saveArchivePage(dbc, gid, arcWeek, 0, "att-"+gid, []string{"Name" + gid}); err != nil {
			t.Fatalf("insert page for %s: %v", gid, err)
		}
	}

	if err := deleteArchivePageRows(dbc, arcGuildA, arcWeek); err != nil {
		t.Fatalf("clear guild A pages: %v", err)
	}
	if _, _, pages, _ := loadGuildScreenshotArchive(dbc, arcGuildA, arcWeek); len(pages) != 0 {
		t.Fatalf("guild A pages survived the clear: %v", pages)
	}
	_, messageID, pages, err := loadGuildScreenshotArchive(dbc, arcGuildB, arcWeek)
	if err != nil {
		t.Fatalf("load guild B: %v", err)
	}
	if messageID != "msg-"+arcGuildB || len(pages) != 1 {
		t.Fatalf("guild B archive affected by guild A clear: %q %v", messageID, pages)
	}
}

// The page ordering contract: oldest updated_at first (that is the cap's
// drop order), id as the tie-break.
func TestScreenshotArchivePageOrdering(t *testing.T) {
	dbc := testdb.TestDB(t)
	if _, err := dbc.Exec(
		`INSERT INTO weekly_screenshot_archives (guild_id, culvert_date, channel_id, message_id) VALUES ($1, $2, 'chan', 'msg')`,
		arcGuildA, arcWeek); err != nil {
		t.Fatalf("insert record: %v", err)
	}
	// Explicit timestamps: page "new" was updated later than page "old".
	if _, err := dbc.Exec(
		`INSERT INTO weekly_screenshot_pages (guild_id, culvert_date, attachment_id, names, updated_at)
		 VALUES ($1, $2, 'att-new', 'x', $3), ($1, $2, 'att-old', 'y', $4)`,
		arcGuildA, arcWeek,
		time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("insert pages: %v", err)
	}
	_, _, pages, err := loadGuildScreenshotArchive(dbc, arcGuildA, arcWeek)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(pages) != 2 || pages[0].attachmentID != "att-old" || pages[1].attachmentID != "att-new" {
		t.Fatalf("pages not ordered oldest-first: %+v", pages)
	}
}
