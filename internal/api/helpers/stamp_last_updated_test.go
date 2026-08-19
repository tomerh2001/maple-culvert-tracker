package helpers

import (
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

// A known write time is stamped as the embed's own footer + timestamp pair,
// which Discord renders in each reader's timezone.
func TestStampLastUpdatedUsesWriteTime(t *testing.T) {
	at := time.Date(2026, 8, 18, 19, 5, 0, 0, time.UTC)
	e := &discordgo.MessageEmbed{}
	StampLastUpdated(e, at, "2026-08-12")

	if e.Footer == nil || e.Footer.Text != "Scores last updated" {
		t.Fatalf("footer = %#v, want text %q", e.Footer, "Scores last updated")
	}
	if want := at.Format(time.RFC3339); e.Timestamp != want {
		t.Fatalf("timestamp = %q, want %q", e.Timestamp, want)
	}
}

// The write time is normalized to UTC so the stamp never depends on the
// process timezone.
func TestStampLastUpdatedNormalizesToUTC(t *testing.T) {
	zone := time.FixedZone("UTC+3", 3*60*60)
	at := time.Date(2026, 8, 18, 22, 5, 0, 0, zone)
	e := &discordgo.MessageEmbed{}
	StampLastUpdated(e, at, "")

	if want := "2026-08-18T19:05:00Z"; e.Timestamp != want {
		t.Fatalf("timestamp = %q, want %q", e.Timestamp, want)
	}
}

// Scores written before db_migrations/8 carry no write time: the footer falls
// back to the newest scored week instead of inventing an instant.
func TestStampLastUpdatedFallsBackToLatestWeek(t *testing.T) {
	e := &discordgo.MessageEmbed{}
	StampLastUpdated(e, time.Time{}, "2026-08-12")

	if e.Footer == nil || e.Footer.Text != "Latest scored week: 2026-08-12" {
		t.Fatalf("footer = %#v, want the latest-week fallback", e.Footer)
	}
	if e.Timestamp != "" {
		t.Fatalf("timestamp = %q, want empty - no write time is known", e.Timestamp)
	}
}

// With nothing known the embed is left exactly as it was.
func TestStampLastUpdatedNoFreshnessLeavesEmbedUntouched(t *testing.T) {
	e := &discordgo.MessageEmbed{}
	StampLastUpdated(e, time.Time{}, "")

	if e.Footer != nil || e.Timestamp != "" {
		t.Fatalf("embed stamped with nothing known: footer=%#v timestamp=%q", e.Footer, e.Timestamp)
	}
}

// A nil embed is a no-op, not a panic - callers stamp whatever the chart path
// produced.
func TestStampLastUpdatedNilEmbed(t *testing.T) {
	StampLastUpdated(nil, time.Now(), "2026-08-12")
}
