package helpers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db/testdb"
)

const recapWeek = "2026-09-09"

func recapSeedAnnouncement(t *testing.T, dbc *sql.DB, guildID, week string) {
	t.Helper()
	if _, err := dbc.Exec(`INSERT INTO weekly_announcements (guild_id, culvert_date, channel_id, message_id)
		VALUES ($1, $2, $3, $4)`, guildID, week, "channel-"+guildID, "original-"+guildID+"-"+week); err != nil {
		t.Fatal(err)
	}
}

func recapSeedCharacter(t *testing.T, dbc *sql.DB, tenantID, name, owner string, scores map[string]int) {
	t.Helper()
	var id int64
	if err := dbc.QueryRow(`INSERT INTO characters (guild_id, maple_character_name, discord_user_id)
		VALUES ($1, $2, $3) RETURNING id`, tenantID, name, owner).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for week, score := range scores {
		if _, err := dbc.Exec(`INSERT INTO character_culvert_scores (character_id, culvert_date, score)
			VALUES ($1, $2, $3)`, id, week, score); err != nil {
			t.Fatal(err)
		}
	}
}

type recapPayload struct {
	Embeds          []*discordgo.MessageEmbed         `json:"embeds"`
	AllowedMentions *discordgo.MessageAllowedMentions `json:"allowed_mentions"`
	Nonce           string                            `json:"nonce"`
	EnforceNonce    bool                              `json:"enforce_nonce"`
}

func recapDecodePayload(t *testing.T, req *http.Request) recapPayload {
	t.Helper()
	var payload recapPayload
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if !payload.EnforceNonce || payload.Nonce == "" || len(payload.Nonce) > 25 {
		t.Fatalf("recap send must enforce a valid nonce: %+v", payload)
	}
	if payload.AllowedMentions == nil || len(payload.AllowedMentions.Parse) != 0 ||
		len(payload.AllowedMentions.Users) != 0 || len(payload.AllowedMentions.Roles) != 0 {
		t.Fatalf("recap must not ping members again: %+v", payload.AllowedMentions)
	}
	return payload
}

func recapJSONResponse(body any) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return archiveResponse(http.StatusOK, string(raw))
}

// A reusable fake records the actual Discord payloads by destination channel.
// A message-started thread uses the summary message's ID, as Discord does.
func recapRecordingSession(t *testing.T) (*discordgo.Session, map[string][]recapPayload) {
	t.Helper()
	recorded := map[string][]recapPayload{}
	s := archiveTestSession(t, func(req *http.Request) (*http.Response, error) {
		parts := strings.Split(strings.Trim(req.URL.Path, "/"), "/")
		if req.Method != http.MethodPost {
			t.Fatalf("unexpected Discord request: %s %s", req.Method, req.URL.Path)
		}
		if strings.HasSuffix(req.URL.Path, "/threads") {
			var start discordgo.ThreadStart
			if err := json.NewDecoder(req.Body).Decode(&start); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(start.Name, "Culvert recap ") || start.AutoArchiveDuration != 10080 {
				t.Fatalf("incorrect recap thread: %+v", start)
			}
			return recapJSONResponse(&discordgo.Channel{ID: parts[len(parts)-2]})
		}
		if !strings.HasSuffix(req.URL.Path, "/messages") {
			t.Fatalf("unexpected Discord endpoint: %s", req.URL.Path)
		}
		channelID := parts[len(parts)-2]
		payload := recapDecodePayload(t, req)
		recorded[channelID] = append(recorded[channelID], payload)
		return recapJSONResponse(&discordgo.Message{ID: "posted-" + channelID, ChannelID: channelID})
	})
	return s, recorded
}

func TestAnnounceWeeklyRecapsResetBoundaryAndRepeatedRuns(t *testing.T) {
	for _, tc := range []struct {
		name, now, wantWeek string
	}{
		{"before reset", "2026-09-16T23:59:59Z", "2026-09-02"},
		{"at reset", "2026-09-17T00:00:00Z", recapWeek},
		{"Israel summer reset", "2026-09-17T03:00:00+03:00", recapWeek},
		{"before Israel winter reset", "2026-12-03T01:59:59+02:00", "2026-11-18"},
		{"at Israel winter reset", "2026-12-03T02:00:00+02:00", "2026-11-25"},
		{"restart after reset", "2026-09-20T12:00:00Z", recapWeek},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbc := testdb.TestDB(t)
			t.Setenv(data.EnvVarDiscordGuildID, "home")
			t.Setenv(data.EnvVarDiscordExtraGuildIDs, "")
			wantWeek, err := time.Parse(time.DateOnly, tc.wantWeek)
			if err != nil {
				t.Fatal(err)
			}
			scores := map[string]int{
				wantWeek.AddDate(0, 0, -7).Format(time.DateOnly): 100,
				tc.wantWeek: 200,
				wantWeek.AddDate(0, 0, 7).Format(time.DateOnly): 300,
			}
			for week := range scores {
				recapSeedAnnouncement(t, dbc, "home", week)
			}
			recapSeedCharacter(t, dbc, "home", "BoundaryHero", "2", scores)
			now, err := time.Parse(time.RFC3339, tc.now)
			if err != nil {
				t.Fatal(err)
			}
			s, messages := recapRecordingSession(t)
			for i := 0; i < 2; i++ {
				if err := AnnounceWeeklyRecaps(context.Background(), s, dbc, nil, []string{"", "home", "home"}, now); err != nil {
					t.Fatal(err)
				}
			}
			if len(messages["channel-home"]) != 1 || len(messages["posted-channel-home"]) != 1 || len(messages) != 2 {
				t.Fatalf("expected one summary and one details message after repeated runs: %+v", messages)
			}
			summary := messages["channel-home"][0]
			if len(summary.Embeds) != 1 || summary.Embeds[0].Title != "Culvert recap - Week of "+tc.wantWeek {
				t.Fatalf("wrong completed week: %+v", summary.Embeds)
			}
			var count int
			if err := dbc.QueryRow(`SELECT COUNT(*) FROM weekly_recaps WHERE culvert_date = $1`, tc.wantWeek).Scan(&count); err != nil || count != 1 {
				t.Fatalf("recap row count = %d, err = %v", count, err)
			}
		})
	}
}

func TestAnnounceWeeklyRecapsSkipsWeeksWithoutVisibleSubmissions(t *testing.T) {
	for _, tc := range []struct {
		name         string
		announcement bool
		owner        string
		week         string
	}{
		{name: "no scores", announcement: true, week: recapWeek},
		{name: "untracked scores", announcement: true, owner: "1", week: recapWeek},
		{name: "scores without submission message", owner: "2", week: recapWeek},
		{name: "older history is not replayed", announcement: true, owner: "2", week: "2026-09-02"},
		{name: "current week is not complete", announcement: true, owner: "2", week: "2026-09-16"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbc := testdb.TestDB(t)
			t.Setenv(data.EnvVarDiscordGuildID, "home")
			t.Setenv(data.EnvVarDiscordExtraGuildIDs, "")
			if tc.announcement {
				recapSeedAnnouncement(t, dbc, "home", tc.week)
			}
			if tc.owner != "" {
				recapSeedCharacter(t, dbc, "home", "SkippedHero", tc.owner, map[string]int{tc.week: 100})
			}
			s := archiveTestSession(t, func(req *http.Request) (*http.Response, error) {
				t.Fatalf("ineligible week called Discord: %s %s", req.Method, req.URL.Path)
				return nil, nil
			})
			if err := AnnounceWeeklyRecaps(context.Background(), s, dbc, nil, []string{"home"}, time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := dbc.QueryRow(`SELECT COUNT(*) FROM weekly_recaps`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("ineligible week stored %d recap rows, err = %v", count, err)
			}
		})
	}
}

func TestAnnounceWeeklyRecapsSharedGuildsAndForeignTenant(t *testing.T) {
	dbc := testdb.TestDB(t)
	t.Setenv(data.EnvVarDiscordGuildID, "home")
	t.Setenv(data.EnvVarDiscordExtraGuildIDs, "shared,absent")
	for _, guildID := range []string{"home", "shared", "foreign", "absent"} {
		recapSeedAnnouncement(t, dbc, guildID, recapWeek)
	}
	recapSeedCharacter(t, dbc, "home", "SharedHero", "2", map[string]int{"2026-09-02": 1000, recapWeek: 1234})
	recapSeedCharacter(t, dbc, "foreign", "ForeignHero", "2", map[string]int{"2026-09-02": 5000, recapWeek: 9999})
	s, messages := recapRecordingSession(t)
	if err := AnnounceWeeklyRecaps(context.Background(), s, dbc, nil, []string{"home", "shared", "foreign"}, time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	nonces := map[string]bool{}
	for _, guildID := range []string{"home", "shared", "foreign"} {
		channel := "channel-" + guildID
		if len(messages[channel]) != 1 || len(messages["posted-"+channel]) != 1 {
			t.Fatalf("guild %s missing recap artifacts: %+v", guildID, messages)
		}
		summary := messages[channel][0]
		details := messages["posted-"+channel][0]
		wantName, unwantedName, wantTotal, wantPrevious := "SharedHero", "ForeignHero", "1,234", "1,000"
		if guildID == "foreign" {
			wantName, unwantedName, wantTotal, wantPrevious = "ForeignHero", "SharedHero", "9,999", "5,000"
		}
		if len(summary.Embeds) != 1 || summary.Embeds[0].Title != "Culvert recap - Week of "+recapWeek ||
			summary.Embeds[0].Description != "Guild total: **"+wantTotal+"**" || len(summary.Embeds[0].Fields) != 2 {
			t.Fatalf("guild %s summary layout/data incorrect: %+v", guildID, summary.Embeds)
		}
		if len(details.Embeds) != 2 || details.Embeds[0].Title != "Top 25 - week of "+recapWeek ||
			details.Embeds[1].Title != "New personal bests this week :tada:" || len(details.Embeds[1].Fields) != 3 ||
			details.Embeds[1].Fields[1].Value != wantPrevious || details.Embeds[1].Fields[2].Value != wantTotal {
			t.Fatalf("guild %s must receive the table and personal bests together: %+v", guildID, details.Embeds)
		}
		for _, payload := range []recapPayload{summary, details} {
			raw, err := json.Marshal(payload.Embeds)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), wantName) || strings.Contains(string(raw), unwantedName) {
				t.Fatalf("guild %s leaked or lost tenant data: %s", guildID, raw)
			}
			if nonces[payload.Nonce] {
				t.Fatalf("different guild/artifact reused nonce %q", payload.Nonce)
			}
			nonces[payload.Nonce] = true
		}
	}
	if len(messages) != 6 {
		t.Fatalf("posted into a guild absent from the bot's guild list: %+v", messages)
	}
	var originalID string
	if err := dbc.QueryRow(`SELECT message_id FROM weekly_announcements WHERE guild_id = 'home' AND culvert_date = $1`, recapWeek).Scan(&originalID); err != nil || originalID != "original-home-"+recapWeek {
		t.Fatalf("original weekly message was changed: %q, err = %v", originalID, err)
	}
}

func recapTestArtifacts() weeklyArtifacts {
	return weeklyArtifacts{
		weekStr:      recapWeek,
		summaryEmbed: &discordgo.MessageEmbed{Title: "Culvert recap - Week of " + recapWeek},
		tableEmbed:   &discordgo.MessageEmbed{Title: "Top 25 - week of " + recapWeek},
		pbEmbed:      &discordgo.MessageEmbed{Title: "New personal bests this week :tada:"},
	}
}

func recapStoredIDs(t *testing.T, dbc *sql.DB) []string {
	t.Helper()
	ids := make([]string, 3)
	if err := dbc.QueryRow(`SELECT message_id, thread_id, table_message_id FROM weekly_recaps
		WHERE guild_id = 'home' AND culvert_date = $1`, recapWeek).Scan(&ids[0], &ids[1], &ids[2]); err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestPostWeeklyRecapRetriesOnlyIncompleteSteps(t *testing.T) {
	for _, failedStage := range []string{"summary", "thread", "details"} {
		t.Run(failedStage, func(t *testing.T) {
			dbc := testdb.TestDB(t)
			failed := false
			calls := map[string]int{}
			nonces := map[string][]string{}
			s := archiveTestSession(t, func(req *http.Request) (*http.Response, error) {
				stage := ""
				switch {
				case strings.HasSuffix(req.URL.Path, "/channels/channel-home/messages"):
					stage = "summary"
				case strings.HasSuffix(req.URL.Path, "/channels/channel-home/messages/summary-id/threads"):
					stage = "thread"
				case strings.HasSuffix(req.URL.Path, "/channels/summary-id/messages"):
					stage = "details"
				default:
					t.Fatalf("retry must use the persisted channel and message: %s %s", req.Method, req.URL.Path)
				}
				calls[stage]++
				if stage != "thread" {
					payload := recapDecodePayload(t, req)
					nonces[stage] = append(nonces[stage], payload.Nonce)
				}
				if stage == failedStage && !failed {
					failed = true
					return archiveResponse(http.StatusForbidden, `{"code":50013,"message":"Missing Permissions"}`)
				}
				if stage == "thread" {
					return recapJSONResponse(&discordgo.Channel{ID: "summary-id"})
				}
				return recapJSONResponse(&discordgo.Message{ID: stage + "-id"})
			})
			art := recapTestArtifacts()
			if err := postWeeklyRecap(context.Background(), s, dbc, "home", "channel-home", art); err == nil {
				t.Fatal("failed Discord step must surface an error")
			}
			wantProgress := map[string][]string{
				"summary": {"", "", ""}, "thread": {"summary-id", "", ""}, "details": {"summary-id", "summary-id", ""},
			}[failedStage]
			if ids := recapStoredIDs(t, dbc); !reflect.DeepEqual(ids, wantProgress) {
				t.Fatalf("lost successful steps: got %v, want %v", ids, wantProgress)
			}
			for i := 0; i < 2; i++ {
				if err := postWeeklyRecap(context.Background(), s, dbc, "home", "new-configured-channel", art); err != nil {
					t.Fatal(err)
				}
			}
			if ids := recapStoredIDs(t, dbc); !reflect.DeepEqual(ids, []string{"summary-id", "summary-id", "details-id"}) {
				t.Fatalf("recap did not finish: %v", ids)
			}
			for _, stage := range []string{"summary", "thread", "details"} {
				wantCalls := 1
				if stage == failedStage {
					wantCalls = 2
				}
				if calls[stage] != wantCalls {
					t.Errorf("%s called %d times, want %d", stage, calls[stage], wantCalls)
				}
				if sent := nonces[stage]; len(sent) == 2 && sent[0] != sent[1] {
					t.Errorf("%s retry changed nonce: %v", stage, sent)
				}
			}
		})
	}
}

func TestPostWeeklyRecapRecoversExistingThread(t *testing.T) {
	dbc := testdb.TestDB(t)
	if _, err := dbc.Exec(`INSERT INTO weekly_recaps (guild_id, culvert_date, channel_id, message_id)
		VALUES ('home', $1, 'channel-home', 'summary-id')`, recapWeek); err != nil {
		t.Fatal(err)
	}
	var calls []string
	s := archiveTestSession(t, func(req *http.Request) (*http.Response, error) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		switch {
		case strings.HasSuffix(req.URL.Path, "/messages/summary-id/threads"):
			return archiveResponse(http.StatusBadRequest, `{"code":160004,"message":"A thread has already been created for this message"}`)
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/channels/summary-id"):
			return recapJSONResponse(&discordgo.Channel{ID: "summary-id"})
		case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/channels/summary-id/messages"):
			recapDecodePayload(t, req)
			return recapJSONResponse(&discordgo.Message{ID: "details-id"})
		default:
			t.Fatalf("recovery duplicated the summary or used a wrong thread: %s %s", req.Method, req.URL.Path)
			return nil, nil
		}
	})
	if err := postWeeklyRecap(context.Background(), s, dbc, "home", "channel-home", recapTestArtifacts()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || !reflect.DeepEqual(recapStoredIDs(t, dbc), []string{"summary-id", "summary-id", "details-id"}) {
		t.Fatalf("thread recovery incomplete, calls = %v", calls)
	}
}

func TestPostWeeklyRecapOverlappingRunCannotDuplicateSummary(t *testing.T) {
	dbc := testdb.TestDB(t)
	art := recapTestArtifacts()
	competing := archiveTestSession(t, func(req *http.Request) (*http.Response, error) {
		t.Fatalf("overlapping worker reached Discord: %s %s", req.Method, req.URL.Path)
		return nil, nil
	})
	entered := false
	s := archiveTestSession(t, func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/channels/channel-home/messages") {
			if entered {
				t.Fatal("summary sent twice")
			}
			entered = true
			// The first worker holds its session lock while the second obtains
			// a different pooled connection and attempts the same guild/week.
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := postWeeklyRecap(ctx, competing, dbc, "home", "channel-home", art); err != nil {
				t.Fatalf("overlapping worker must skip promptly: %v", err)
			}
			return recapJSONResponse(&discordgo.Message{ID: "summary-id"})
		}
		if strings.HasSuffix(req.URL.Path, "/threads") {
			return recapJSONResponse(&discordgo.Channel{ID: "summary-id"})
		}
		return recapJSONResponse(&discordgo.Message{ID: "details-id"})
	})
	if err := postWeeklyRecap(context.Background(), s, dbc, "home", "channel-home", art); err != nil {
		t.Fatal(err)
	}
	if !entered || !reflect.DeepEqual(recapStoredIDs(t, dbc), []string{"summary-id", "summary-id", "details-id"}) {
		t.Fatal("first worker failed to complete its recap")
	}
}

func TestSendRecapMessageNonceSurvivesNewSession(t *testing.T) {
	var nonces []string
	for i, identity := range []string{"home:" + recapWeek + ":summary", "home:" + recapWeek + ":summary", "home:" + recapWeek + ":table"} {
		s := archiveTestSession(t, func(req *http.Request) (*http.Response, error) {
			payload := recapDecodePayload(t, req)
			nonces = append(nonces, payload.Nonce)
			return recapJSONResponse(&discordgo.Message{ID: fmt.Sprintf("message-%d", i)})
		})
		if _, err := sendRecapMessage(context.Background(), s, "channel-home", identity, []*discordgo.MessageEmbed{{Title: "Recap"}}); err != nil {
			t.Fatal(err)
		}
	}
	if nonces[0] != nonces[1] || nonces[0] == nonces[2] {
		t.Fatalf("nonce must survive a new session and distinguish artifacts: %v", nonces)
	}
}
