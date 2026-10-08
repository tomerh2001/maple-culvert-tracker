package helpers

import (
	"database/sql"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/tomerh2001/maple-culvert-tracker/internal/db/testdb"
)

func seedWeeklyAnnouncementRetry(t *testing.T, dbc *sql.DB) {
	t.Helper()
	if _, err := dbc.Exec(`INSERT INTO weekly_announcements
		(guild_id, culvert_date, channel_id, message_id, thread_id, table_message_id)
		VALUES ('home', $1, 'channel-home', 'original-summary', 'original-thread', 'original-details')`, recapWeek); err != nil {
		t.Fatal(err)
	}
}

func TestWeeklyAnnouncementEditFailurePreservesMessage(t *testing.T) {
	for _, failure := range []struct {
		name   string
		status int
		body   string
	}{
		{"permissions", http.StatusForbidden, `{"code":50013,"message":"Missing Permissions"}`},
		{"server error", http.StatusInternalServerError, `{"message":"Internal Server Error"}`},
		{"unknown channel", http.StatusNotFound, `{"code":10003,"message":"Unknown Channel"}`},
		{"transport", 0, ""},
	} {
		for _, refresh := range []bool{false, true} {
			mode := "submission"
			if refresh {
				mode = "refresh"
			}
			t.Run(failure.name+"/"+mode, func(t *testing.T) {
				dbc := testdb.TestDB(t)
				seedWeeklyAnnouncementRetry(t, dbc)
				patches := 0
				s := archiveTestSession(t, func(req *http.Request) (*http.Response, error) {
					if req.Method != http.MethodPatch || !strings.HasSuffix(req.URL.Path, "/channels/channel-home/messages/original-summary") {
						t.Fatalf("failed edit must not send a replacement: %s %s", req.Method, req.URL.Path)
					}
					patches++
					if failure.status == 0 {
						return nil, errors.New("connection interrupted")
					}
					return archiveResponse(failure.status, failure.body)
				})
				if _, err := upsertWeeklyArtifacts(s, dbc, "home", "new-channel", recapTestArtifacts(), refresh); err == nil {
					t.Fatal("failed edit must surface an error")
				}
				var channelID, messageID, threadID, tableID string
				if err := dbc.QueryRow(`SELECT channel_id, message_id, thread_id, table_message_id
					FROM weekly_announcements WHERE guild_id = 'home' AND culvert_date = $1`, recapWeek).
					Scan(&channelID, &messageID, &threadID, &tableID); err != nil {
					t.Fatal(err)
				}
				if got, want := []string{channelID, messageID, threadID, tableID}, []string{"channel-home", "original-summary", "original-thread", "original-details"}; !reflect.DeepEqual(got, want) {
					t.Fatalf("failed edit changed stored IDs: got %v, want %v", got, want)
				}
				if patches != 1 {
					t.Fatalf("expected one edit attempt, got %d", patches)
				}
			})
		}
	}
}

func TestWeeklyAnnouncementConfirmedDeletion(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		mode := "submission"
		if refresh {
			mode = "refresh"
		}
		t.Run(mode, func(t *testing.T) {
			dbc := testdb.TestDB(t)
			seedWeeklyAnnouncementRetry(t, dbc)
			var calls []string
			s := archiveTestSession(t, func(req *http.Request) (*http.Response, error) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				if req.Method == http.MethodPatch && strings.HasSuffix(req.URL.Path, "/channels/channel-home/messages/original-summary") {
					return archiveResponse(http.StatusNotFound, `{"code":10008,"message":"Unknown Message"}`)
				}
				if refresh {
					t.Fatalf("refresh must not recreate a deleted message: %s %s", req.Method, req.URL.Path)
				}
				if req.Method == http.MethodPost {
					switch {
					case strings.HasSuffix(req.URL.Path, "/channels/channel-home/messages"):
						return archiveResponse(http.StatusOK, `{"id":"replacement-summary"}`)
					case strings.HasSuffix(req.URL.Path, "/channels/channel-home/messages/replacement-summary/threads"):
						return archiveResponse(http.StatusOK, `{"id":"replacement-summary"}`)
					case strings.HasSuffix(req.URL.Path, "/channels/replacement-summary/messages"):
						return archiveResponse(http.StatusOK, `{"id":"replacement-details"}`)
					}
				}
				t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
				return nil, nil
			})
			threadID, err := upsertWeeklyArtifacts(s, dbc, "home", "", recapTestArtifacts(), refresh)
			if err != nil {
				t.Fatal(err)
			}
			var channelID, messageID, storedThread, tableID string
			err = dbc.QueryRow(`SELECT channel_id, message_id, thread_id, table_message_id
				FROM weekly_announcements WHERE guild_id = 'home' AND culvert_date = $1`, recapWeek).
				Scan(&channelID, &messageID, &storedThread, &tableID)
			if refresh {
				if !errors.Is(err, sql.ErrNoRows) || threadID != "" || len(calls) != 1 {
					t.Fatalf("refresh must only forget the deleted message: thread=%q, err=%v, calls=%v", threadID, err, calls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, want := []string{channelID, messageID, storedThread, tableID}, []string{"channel-home", "replacement-summary", "replacement-summary", "replacement-details"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("submission did not record replacement IDs: got %v, want %v", got, want)
			}
			if threadID != "replacement-summary" || len(calls) != 4 {
				t.Fatalf("expected one replacement summary, thread, and details: thread=%q, calls=%v", threadID, calls)
			}
		})
	}
}
