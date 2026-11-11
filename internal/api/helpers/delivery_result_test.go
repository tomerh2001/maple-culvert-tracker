package helpers

import (
	"context"
	"errors"
	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db/testdb"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestThreadArtifactUncertainEditsNeverRepost(t *testing.T) {
	for _, failure := range archiveFailureCases {
		t.Run(failure.name, func(t *testing.T) {
			db := testdb.TestDB(t)
			calls := 0
			session := archiveTestSession(t, func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != http.MethodPatch {
					t.Fatal("uncertain edit created another message")
				}
				if failure.status == 0 {
					return nil, errors.New("uncertain transport")
				}
				return archiveResponse(failure.status, failure.body)
			})
			err := upsertThreadArtifacts(session, db, "guild", "2026-11-04", "thread", "existing", &discordgo.MessageEmbed{}, &discordgo.MessageEmbed{})
			if err == nil || calls != 1 {
				t.Fatalf("error=%v calls=%d", err, calls)
			}
		})
	}
}
func TestThreadArtifactExplicitDeletionRecreatesAndPersists(t *testing.T) {
	db := testdb.TestDB(t)
	if _, err := db.Exec("INSERT INTO weekly_announcements(guild_id,culvert_date,channel_id,message_id,thread_id) VALUES('guild','2026-11-04','chan','summary','thread')"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	session := archiveTestSession(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method == http.MethodPatch {
			return archiveResponse(404, `{"code":10008,"message":"Unknown Message"}`)
		}
		if req.Method != http.MethodPost {
			t.Fatal("unexpected method")
		}
		return archiveResponse(200, `{"id":"new-message","channel_id":"thread"}`)
	})
	if err := upsertThreadArtifacts(session, db, "guild", "2026-11-04", "thread", "deleted", &discordgo.MessageEmbed{}, &discordgo.MessageEmbed{}); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := db.QueryRow("SELECT table_message_id FROM weekly_announcements").Scan(&id); err != nil || id != "new-message" || calls != 2 {
		t.Fatalf("%s %d %v", id, calls, err)
	}
}
func TestWeeklyPartialThreadFailureIsReportedWithStoredSummary(t *testing.T) {
	db := testdb.TestDB(t)
	session := archiveTestSession(t, func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/threads") {
			return nil, errors.New("thread response lost")
		}
		return archiveResponse(200, `{"id":"summary","channel_id":"chan"}`)
	})
	art := weeklyArtifacts{weekStr: "2026-11-04", summaryEmbed: &discordgo.MessageEmbed{}}
	if _, err := upsertWeeklyArtifacts(session, db, "guild", "chan", art, false); err == nil {
		t.Fatal("partial delivery claimed success")
	}
	var mid string
	if err := db.QueryRow("SELECT message_id FROM weekly_announcements").Scan(&mid); err != nil || mid != "summary" {
		t.Fatalf("summary identity lost %s %v", mid, err)
	}
}
func TestWeeklyPersistenceFailureRetainsActualResponseIDs(t *testing.T) {
	db := testdb.TestDB(t)
	if _, err := db.Exec(`CREATE FUNCTION test_fail_artifact() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture failure'; END $$; CREATE TRIGGER test_artifact_fail BEFORE INSERT ON weekly_announcements FOR EACH ROW EXECUTE FUNCTION test_fail_artifact()`); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("DROP TRIGGER test_artifact_fail ON weekly_announcements; DROP FUNCTION test_fail_artifact()")
	session := archiveTestSession(t, func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/threads") {
			return archiveResponse(200, `{"id":"thread"}`)
		}
		return archiveResponse(200, `{"id":"summary","channel_id":"chan"}`)
	})
	_, err := upsertWeeklyArtifacts(session, db, "guild", "chan", weeklyArtifacts{weekStr: "2026-11-04", summaryEmbed: &discordgo.MessageEmbed{}}, false)
	refs := KnownDeliveryMessages(err)
	if err == nil || len(refs) != 1 || refs[0].MessageID != "summary" || refs[0].ThreadID != "thread" {
		t.Fatalf("partial identity missing %+v %v", refs, err)
	}
}
func TestDeliveryContextReachesDiscordRequests(t *testing.T) {
	db := testdb.TestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	calls := 0
	session := archiveTestSession(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Context() != ctx {
			t.Error("delivery context missing")
		}
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	started := time.Now()
	err := upsertThreadArtifacts(session, db, "guild", "2026-11-04", "thread", "existing", &discordgo.MessageEmbed{}, &discordgo.MessageEmbed{}, discordgo.WithContext(ctx))
	if err == nil || calls != 1 || time.Since(started) > time.Second {
		t.Fatalf("deadline not honored calls=%d err=%v", calls, err)
	}
}
