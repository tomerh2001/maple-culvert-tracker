package helpers

// Exercise discordgo's actual JSON/multipart encoding and error decoding with
// an in-memory HTTP transport. Database tests use the disposable TestDB harness;
// none of these tests connect to Discord.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db/testdb"
)

type archiveTransport func(*http.Request) (*http.Response, error)

func (transport archiveTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return transport(req)
}

func archiveTestSession(t *testing.T, handler archiveTransport) *discordgo.Session {
	t.Helper()
	s, err := discordgo.New("Bot archive-test")
	if err != nil {
		t.Fatal(err)
	}
	s.Client = &http.Client{Transport: handler}
	s.MaxRestRetries = 0
	s.ShouldRetryOnRateLimit = false
	return s
}

func archiveResponse(status int, body string) (*http.Response, error) {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func archiveMessageResponse(t *testing.T, id string, attachments []*discordgo.MessageAttachment) (*http.Response, error) {
	t.Helper()
	body, err := json.Marshal(&discordgo.Message{ID: id, ChannelID: "chan", Attachments: attachments})
	if err != nil {
		t.Fatal(err)
	}
	return archiveResponse(http.StatusOK, string(body))
}

func seedDiscordArchive(t *testing.T, dbc *sql.DB) ([]storedArchivePage, []*discordgo.MessageAttachment) {
	t.Helper()
	if _, err := dbc.Exec(
		`INSERT INTO weekly_screenshot_archives (guild_id, culvert_date, channel_id, message_id) VALUES ($1, $2, 'chan', 'msg')`,
		arcGuildA, arcWeek); err != nil {
		t.Fatal(err)
	}
	attachments := []*discordgo.MessageAttachment{}
	for i, name := range []string{"Alpha", "Beta", "Gamma"} {
		id := fmt.Sprintf("100%d", i)
		if err := saveArchivePage(dbc, arcGuildA, arcWeek, 0, id, []string{name}); err != nil {
			t.Fatal(err)
		}
		attachments = append(attachments, &discordgo.MessageAttachment{ID: id, Filename: name + ".png"})
	}
	_, _, pages, err := loadGuildScreenshotArchive(dbc, arcGuildA, arcWeek)
	if err != nil {
		t.Fatal(err)
	}
	return pages, attachments
}

func assertDiscordArchiveUnchanged(t *testing.T, dbc *sql.DB, before []storedArchivePage) {
	t.Helper()
	channelID, messageID, after, err := loadGuildScreenshotArchive(dbc, arcGuildA, arcWeek)
	if err != nil {
		t.Fatal(err)
	}
	if channelID != "chan" || messageID != "msg" || !reflect.DeepEqual(before, after) {
		t.Fatalf("archive changed after failed Discord request: %q/%q %+v, originally %+v", channelID, messageID, after, before)
	}
}

// Parse the actual multipart payload sent by discordgo, including upload bytes.
func archiveUpload(t *testing.T, req *http.Request) (discordgo.MessageEdit, string) {
	t.Helper()
	if err := req.ParseMultipartForm(1 << 20); err != nil {
		t.Fatalf("parse multipart upload: %v", err)
	}
	defer req.MultipartForm.RemoveAll()
	var edit discordgo.MessageEdit
	if err := json.Unmarshal([]byte(req.FormValue("payload_json")), &edit); err != nil {
		t.Fatalf("decode payload_json: %v", err)
	}
	file, header, err := req.FormFile("files[0]")
	if err != nil {
		t.Fatalf("missing upload: %v", err)
	}
	defer file.Close()
	content, err := io.ReadAll(file)
	if err != nil || !bytes.Equal(content, []byte("new screenshot")) {
		t.Fatalf("upload bytes = %q, err = %v", content, err)
	}
	return edit, header.Filename
}

func TestEditScreenshotArchiveFullReplacement(t *testing.T) {
	patches := 0
	s := archiveTestSession(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPatch {
			t.Fatalf("full replacement should only PATCH, got %s", req.Method)
		}
		patches++
		edit, filename := archiveUpload(t, req)
		want := []*discordgo.MessageAttachment{{ID: "0", Filename: filename}}
		if edit.Attachments == nil || !reflect.DeepEqual(*edit.Attachments, want) {
			t.Fatalf("upload must include its filename: %+v", edit.Attachments)
		}
		return archiveMessageResponse(t, "msg", []*discordgo.MessageAttachment{{ID: "2000", Filename: filename}})
	})
	files := []*discordgo.File{{Name: "new-page.png", Reader: strings.NewReader("new screenshot")}}
	if _, err := editScreenshotArchive(s, "chan", "msg", "1 page(s)", nil, files); err != nil {
		t.Fatal(err)
	}
	if patches != 1 {
		t.Fatalf("patches = %d, want 1", patches)
	}
}

func TestScreenshotArchivePartialUpdateRetainsOtherPages(t *testing.T) {
	dbc := testdb.TestDB(t)
	before, attachments := seedDiscordArchive(t, dbc)
	seedArchiveWeeklyMessage(t, dbc, arcGuildA, arcWeek, "weekly-chan", "weekly-msg")
	var requests []string
	s := archiveTestSession(t, func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.Method)
		if !strings.HasSuffix(req.URL.Path, "/channels/chan/messages/msg") {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
		switch req.Method {
		case http.MethodGet:
			return archiveMessageResponse(t, "msg", attachments)
		case http.MethodPatch:
			edit, filename := archiveUpload(t, req)
			if edit.Attachments == nil || len(*edit.Attachments) != 3 {
				t.Fatalf("expected two surviving attachments and one upload: %+v", edit.Attachments)
			}
			for _, a := range *edit.Attachments {
				// Reproduce Discord's validation of the actual incident payload.
				if a.Filename == "" {
					return archiveResponse(http.StatusBadRequest, `{"code":50035,"message":"Invalid Form Body: filename BASE_TYPE_BAD_LENGTH"}`)
				}
			}
			want := []*discordgo.MessageAttachment{attachments[1], attachments[2], {ID: "0", Filename: filename}}
			if !reflect.DeepEqual(*edit.Attachments, want) {
				t.Fatalf("edit attachments = %+v, want %+v", *edit.Attachments, want)
			}
			if edit.Content == nil || !strings.Contains(*edit.Content, "3 page(s)") {
				t.Fatalf("incorrect page count in content: %v", edit.Content)
			}
			if !strings.Contains(*edit.Content, "\nWeekly Message: https://discord.com/channels/"+arcGuildA+"/weekly-chan/weekly-msg") {
				t.Fatalf("weekly message link missing: %s", *edit.Content)
			}
			return archiveMessageResponse(t, "msg", []*discordgo.MessageAttachment{attachments[1], attachments[2], {ID: "2000", Filename: filename}})
		default:
			t.Fatalf("unexpected request: %s", req.Method)
			return nil, errors.New("unexpected request")
		}
	})
	err := upsertGuildScreenshotArchive(s, dbc, arcGuildA, "chan", arcWeek, []ScreenshotPage{{Bytes: []byte("new screenshot"), Names: []string{"Alpha"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(requests, []string{http.MethodGet, http.MethodPatch}) {
		t.Fatalf("requests = %v, want GET then PATCH without a new message", requests)
	}
	channelID, messageID, after, err := loadGuildScreenshotArchive(dbc, arcGuildA, arcWeek)
	if err != nil || channelID != "chan" || messageID != "msg" || len(after) != 3 {
		t.Fatalf("archive = %q/%q %+v, err = %v", channelID, messageID, after, err)
	}
	for i := 0; i < 2; i++ {
		if !reflect.DeepEqual(after[i], before[i+1]) {
			t.Fatalf("surviving page changed: %+v, want %+v", after[i], before[i+1])
		}
	}
	if after[2].id != before[0].id || after[2].attachmentID != "2000" || !reflect.DeepEqual(after[2].names, before[0].names) {
		t.Fatalf("replacement did not update the original page row: %+v", after[2])
	}
}

var archiveFailureCases = []struct {
	name   string
	status int
	body   string
}{
	{"validation", http.StatusBadRequest, `{"code":50035,"message":"Invalid Form Body"}`},
	{"permission", http.StatusForbidden, `{"code":50013,"message":"Missing Permissions"}`},
	{"unknown_channel", http.StatusNotFound, `{"code":10003,"message":"Unknown Channel"}`},
	{"unclassified_404", http.StatusNotFound, `not found`},
	{"rate_limit", http.StatusTooManyRequests, `{"message":"Rate limited","retry_after":0,"global":false}`},
	{"server", http.StatusInternalServerError, `{"code":0,"message":"Internal Server Error"}`},
	{"transport", 0, ""},
}

func TestScreenshotArchiveFailuresPreserveRecord(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		for _, failure := range archiveFailureCases {
			t.Run(method+"/"+failure.name, func(t *testing.T) {
				dbc := testdb.TestDB(t)
				before, attachments := seedDiscordArchive(t, dbc)
				failed := false
				s := archiveTestSession(t, func(req *http.Request) (*http.Response, error) {
					if req.Method == method {
						failed = true
						if failure.status == 0 {
							return nil, errors.New("connection reset")
						}
						return archiveResponse(failure.status, failure.body)
					}
					if req.Method == http.MethodGet {
						return archiveMessageResponse(t, "msg", attachments)
					}
					t.Fatalf("unexpected request after failure: %s", req.Method)
					return nil, errors.New("unexpected request")
				})
				err := upsertGuildScreenshotArchive(s, dbc, arcGuildA, "chan", arcWeek, []ScreenshotPage{{Bytes: []byte("new screenshot"), Names: []string{"Alpha"}}})
				if !failed || err == nil {
					t.Fatalf("failure reached = %v, err = %v; want a reported archive failure", failed, err)
				}
				assertDiscordArchiveUnchanged(t, dbc, before)
			})
		}
	}
}

func TestScreenshotArchiveDeletedMessageRecreated(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			dbc := testdb.TestDB(t)
			_, attachments := seedDiscordArchive(t, dbc)
			seedArchiveWeeklyMessage(t, dbc, arcGuildA, arcWeek, "weekly-chan", "weekly-msg")
			posts := 0
			s := archiveTestSession(t, func(req *http.Request) (*http.Response, error) {
				if req.Method == method {
					if method == http.MethodPatch {
						archiveUpload(t, req)
					}
					return archiveResponse(http.StatusNotFound, `{"code":10008,"message":"Unknown Message"}`)
				}
				if req.Method == http.MethodGet {
					return archiveMessageResponse(t, "msg", attachments)
				}
				if req.Method != http.MethodPost || !strings.HasSuffix(req.URL.Path, "/channels/chan/messages") {
					t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
				}
				posts++
				edit, filename := archiveUpload(t, req)
				if edit.Content == nil || !strings.Contains(*edit.Content, "1 page(s)") {
					t.Fatalf("recreated content: %v", edit.Content)
				}
				if !strings.Contains(*edit.Content, "\nWeekly Message: https://discord.com/channels/"+arcGuildA+"/weekly-chan/weekly-msg") {
					t.Fatalf("recreated archive lost weekly message link: %s", *edit.Content)
				}
				return archiveMessageResponse(t, "new-msg", []*discordgo.MessageAttachment{{ID: "2000", Filename: filename}})
			})
			// Empty channel config still recreates in the saved channel.
			if err := upsertGuildScreenshotArchive(s, dbc, arcGuildA, "", arcWeek, []ScreenshotPage{{Bytes: []byte("new screenshot"), Names: []string{"Alpha"}}}); err != nil {
				t.Fatal(err)
			}
			channelID, messageID, after, err := loadGuildScreenshotArchive(dbc, arcGuildA, arcWeek)
			if err != nil || posts != 1 || channelID != "chan" || messageID != "new-msg" || len(after) != 1 || after[0].attachmentID != "2000" {
				t.Fatalf("recreated archive = %q/%q %+v, posts = %d, err = %v", channelID, messageID, after, posts, err)
			}
		})
	}
}

func TestScreenshotArchiveMissingSurvivorPreservesRecord(t *testing.T) {
	dbc := testdb.TestDB(t)
	before, _ := seedDiscordArchive(t, dbc)
	s := archiveTestSession(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			t.Fatalf("missing survivor must stop before editing, got %s", req.Method)
		}
		return archiveMessageResponse(t, "msg", nil)
	})
	if err := upsertGuildScreenshotArchive(s, dbc, arcGuildA, "chan", arcWeek, []ScreenshotPage{{Bytes: []byte("new screenshot"), Names: []string{"Alpha"}}}); err == nil {
		t.Fatal("missing survivor was ignored")
	}
	assertDiscordArchiveUnchanged(t, dbc, before)
}

func TestClearScreenshotArchiveFailuresPreserveRecord(t *testing.T) {
	for _, failure := range archiveFailureCases {
		t.Run(failure.name, func(t *testing.T) {
			dbc := testdb.TestDB(t)
			t.Setenv(data.EnvVarDiscordGuildID, "")
			before, _ := seedDiscordArchive(t, dbc)
			s := archiveTestSession(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodPatch {
					t.Fatalf("unexpected request: %s", req.Method)
				}
				if failure.status == 0 {
					return nil, errors.New("connection reset")
				}
				return archiveResponse(failure.status, failure.body)
			})
			week, _ := time.Parse(time.DateOnly, arcWeek)
			if err := ClearWeekScreenshots(s, dbc, arcGuildA, week); err == nil {
				t.Fatal("clear failure was swallowed")
			}
			assertDiscordArchiveUnchanged(t, dbc, before)
		})
	}
}

func TestClearScreenshotArchiveSuccessAndDeletedMessage(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(fmt.Sprintf("deleted=%v", deleted), func(t *testing.T) {
			dbc := testdb.TestDB(t)
			t.Setenv(data.EnvVarDiscordGuildID, "")
			seedDiscordArchive(t, dbc)
			s := archiveTestSession(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodPatch {
					t.Fatalf("unexpected request: %s", req.Method)
				}
				var edit discordgo.MessageEdit
				if err := json.NewDecoder(req.Body).Decode(&edit); err != nil {
					t.Fatal(err)
				}
				if edit.Attachments == nil || len(*edit.Attachments) != 0 {
					t.Fatalf("clear must send an explicit empty attachment list: %+v", edit.Attachments)
				}
				if deleted {
					return archiveResponse(http.StatusNotFound, `{"code":10008,"message":"Unknown Message"}`)
				}
				return archiveMessageResponse(t, "msg", nil)
			})
			week, _ := time.Parse(time.DateOnly, arcWeek)
			if err := ClearWeekScreenshots(s, dbc, arcGuildA, week); err != nil {
				t.Fatal(err)
			}
			_, messageID, after, err := loadGuildScreenshotArchive(dbc, arcGuildA, arcWeek)
			if err != nil || len(after) != 0 || deleted && messageID != "" || !deleted && messageID != "msg" {
				t.Fatalf("cleared archive = %q %+v, err = %v", messageID, after, err)
			}
		})
	}
}

func TestScreenshotArchiveCreatedWithAvailableWeeklyLink(t *testing.T) {
	for _, withWeeklyMessage := range []bool{false, true} {
		t.Run(fmt.Sprintf("weekly_message=%v", withWeeklyMessage), func(t *testing.T) {
			dbc := testdb.TestDB(t)
			if withWeeklyMessage {
				seedArchiveWeeklyMessage(t, dbc, arcGuildA, arcWeek, "weekly-chan", "weekly-msg")
			}
			posts := 0
			s := archiveTestSession(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodPost || !strings.HasSuffix(req.URL.Path, "/channels/archive-chan/messages") {
					t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
				}
				posts++
				edit, filename := archiveUpload(t, req)
				if edit.Content == nil || !strings.Contains(*edit.Content, "1 page(s)") {
					t.Fatalf("archive content: %v", edit.Content)
				}
				if withWeeklyMessage {
					want := "Weekly Message: https://discord.com/channels/" + arcGuildA + "/weekly-chan/weekly-msg"
					if lines := strings.Split(*edit.Content, "\n"); len(lines) != 3 || lines[2] != want {
						t.Fatalf("weekly link must be third row, got %q", *edit.Content)
					}
				} else if strings.Contains(*edit.Content, "Weekly Message:") {
					t.Fatalf("archive fabricated a weekly link: %s", *edit.Content)
				}
				return archiveMessageResponse(t, "new-msg", []*discordgo.MessageAttachment{{ID: "2000", Filename: filename}})
			})
			if err := upsertGuildScreenshotArchive(s, dbc, arcGuildA, "archive-chan", arcWeek, []ScreenshotPage{{Bytes: []byte("new screenshot"), Names: []string{"Alpha"}}}); err != nil {
				t.Fatal(err)
			}
			if posts != 1 {
				t.Fatalf("posts = %d, want 1", posts)
			}
		})
	}
}

func TestRefreshScreenshotLinkPreservesContentAndAttachments(t *testing.T) {
	base := "**Culvert screenshots — week of " + arcWeek + "**\n3 page(s) · last updated <t:1785934800:f>"
	link := "Weekly Message: https://discord.com/channels/" + arcGuildA + "/weekly-chan/weekly-msg"
	for _, tc := range []struct {
		name    string
		content string
		want    string
		patches int
	}{
		{"append", base, base + "\n" + link, 1},
		{"replace", base + "\nWeekly Message: https://discord.com/channels/old/channel/message\n(2 older page(s) dropped)", base + "\n" + link + "\n(2 older page(s) dropped)", 1},
		{"unchanged", base + "\n" + link, base + "\n" + link, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbc := testdb.TestDB(t)
			t.Setenv(data.EnvVarDiscordGuildID, "")
			before, attachments := seedDiscordArchive(t, dbc)
			seedArchiveWeeklyMessage(t, dbc, arcGuildA, arcWeek, "weekly-chan", "weekly-msg")
			gets, patches := 0, 0
			s := archiveTestSession(t, func(req *http.Request) (*http.Response, error) {
				if !strings.HasSuffix(req.URL.Path, "/channels/chan/messages/msg") {
					t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
				}
				switch req.Method {
				case http.MethodGet:
					gets++
					body, err := json.Marshal(&discordgo.Message{ID: "msg", Content: tc.content, Attachments: attachments})
					if err != nil {
						t.Fatal(err)
					}
					return archiveResponse(http.StatusOK, string(body))
				case http.MethodPatch:
					patches++
					var payload map[string]json.RawMessage
					if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
						t.Fatal(err)
					}
					if _, ok := payload["attachments"]; ok {
						t.Fatal("link refresh must omit attachment fields")
					}
					if _, ok := payload["embeds"]; ok {
						t.Fatal("link refresh must omit embed fields")
					}
					var content string
					if err := json.Unmarshal(payload["content"], &content); err != nil || content != tc.want {
						t.Fatalf("updated content = %q, err = %v; want %q", content, err, tc.want)
					}
					return archiveMessageResponse(t, "msg", attachments)
				default:
					t.Fatalf("refresh must not create messages: %s", req.Method)
					return nil, errors.New("unexpected request")
				}
			})
			week, _ := time.Parse(time.DateOnly, arcWeek)
			if err := RefreshWeekScreenshotLinks(s, dbc, arcGuildA, week); err != nil {
				t.Fatal(err)
			}
			if gets != 1 || patches != tc.patches {
				t.Fatalf("requests = %d GET, %d PATCH; want 1 GET, %d PATCH", gets, patches, tc.patches)
			}
			assertDiscordArchiveUnchanged(t, dbc, before)
		})
	}
}

func TestRefreshScreenshotLinkMissingRecordsAreSkipped(t *testing.T) {
	for _, archiveExists := range []bool{false, true} {
		t.Run(fmt.Sprintf("archive=%v", archiveExists), func(t *testing.T) {
			dbc := testdb.TestDB(t)
			t.Setenv(data.EnvVarDiscordGuildID, "")
			if archiveExists {
				seedDiscordArchive(t, dbc)
			} else {
				seedArchiveWeeklyMessage(t, dbc, arcGuildA, arcWeek, "weekly-chan", "weekly-msg")
			}
			s := archiveTestSession(t, func(req *http.Request) (*http.Response, error) {
				t.Fatalf("missing records must not contact Discord: %s %s", req.Method, req.URL.Path)
				return nil, errors.New("unexpected request")
			})
			week, _ := time.Parse(time.DateOnly, arcWeek)
			if err := RefreshWeekScreenshotLinks(s, dbc, arcGuildA, week); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRefreshScreenshotLinkFailuresPreserveRecord(t *testing.T) {
	failures := append([]struct {
		name   string
		status int
		body   string
	}{{"deleted_message", http.StatusNotFound, `{"code":10008,"message":"Unknown Message"}`}}, archiveFailureCases...)
	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		for _, failure := range failures {
			t.Run(method+"/"+failure.name, func(t *testing.T) {
				dbc := testdb.TestDB(t)
				t.Setenv(data.EnvVarDiscordGuildID, "")
				before, attachments := seedDiscordArchive(t, dbc)
				seedArchiveWeeklyMessage(t, dbc, arcGuildA, arcWeek, "weekly-chan", "weekly-msg")
				s := archiveTestSession(t, func(req *http.Request) (*http.Response, error) {
					if req.Method == method {
						if failure.status == 0 {
							return nil, errors.New("connection reset")
						}
						return archiveResponse(failure.status, failure.body)
					}
					if req.Method == http.MethodGet {
						return archiveMessageResponse(t, "msg", attachments)
					}
					t.Fatalf("unexpected request: %s", req.Method)
					return nil, errors.New("unexpected request")
				})
				week, _ := time.Parse(time.DateOnly, arcWeek)
				if err := RefreshWeekScreenshotLinks(s, dbc, arcGuildA, week); err == nil {
					t.Fatal("refresh failure was swallowed")
				}
				assertDiscordArchiveUnchanged(t, dbc, before)
			})
		}
	}
}
