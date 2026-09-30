package helpers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/tomerh2001/maple-culvert-tracker/internal/data"
	"github.com/tomerh2001/maple-culvert-tracker/internal/db/testdb"
)

// archiveDiscordStore simulates Discord's ten-attachment message limit and
// attachment edit semantics, including retained IDs and multipart upload IDs.
// All requests stay in memory, while the page identities use real PostgreSQL.
type archiveDiscordStore struct {
	messages    map[string]*discordgo.Message
	nextID      int
	failMessage string
	requests    []string
}

func archivePartsSession(t *testing.T) (*discordgo.Session, *archiveDiscordStore) {
	t.Helper()
	store := &archiveDiscordStore{messages: make(map[string]*discordgo.Message), nextID: 1000}
	s := archiveTestSession(t, func(req *http.Request) (*http.Response, error) {
		id := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
		store.requests = append(store.requests, req.Method+" "+id)
		if id == store.failMessage {
			return archiveResponse(http.StatusForbidden, `{"code":50013,"message":"Missing Permissions"}`)
		}
		previous := store.messages[id]
		if req.Method != http.MethodPost && previous == nil {
			return archiveResponse(http.StatusNotFound, `{"code":10008,"message":"Unknown Message"}`)
		}
		if req.Method == http.MethodGet {
			return archiveMessageResponse(t, id, previous.Attachments)
		}
		var edit discordgo.MessageEdit
		uploads := make(map[string]string)
		if strings.HasPrefix(req.Header.Get("Content-Type"), "multipart/") {
			if err := req.ParseMultipartForm(1 << 20); err != nil {
				t.Fatal(err)
			}
			defer req.MultipartForm.RemoveAll()
			if err := json.Unmarshal([]byte(req.FormValue("payload_json")), &edit); err != nil {
				t.Fatal(err)
			}
			for key, files := range req.MultipartForm.File {
				index := strings.TrimSuffix(strings.TrimPrefix(key, "files["), "]")
				for _, file := range files {
					f, err := file.Open()
					if err != nil {
						t.Fatal(err)
					}
					body, err := io.ReadAll(f)
					f.Close()
					if err != nil || len(body) == 0 {
						t.Fatalf("empty screenshot upload: %v", err)
					}
					uploads[index] = file.Filename
				}
			}
		} else if err := json.NewDecoder(req.Body).Decode(&edit); err != nil {
			t.Fatal(err)
		}
		if req.Method == http.MethodPost {
			store.nextID++
			id = strconv.Itoa(store.nextID)
		}
		attachments := make([]*discordgo.MessageAttachment, 0)
		if edit.Attachments != nil {
			for _, a := range *edit.Attachments {
				if _, fresh := uploads[a.ID]; fresh {
					continue
				}
				found := false
				if previous != nil {
					for _, old := range previous.Attachments {
						if old.ID == a.ID && old.Filename == a.Filename {
							found = true
						}
					}
				}
				if !found {
					t.Fatalf("retaining invalid attachment: %+v", a)
				}
				attachments = append(attachments, a)
			}
		}
		indices := make([]int, 0, len(uploads))
		for key := range uploads {
			n, _ := strconv.Atoi(key)
			indices = append(indices, n)
		}
		sort.Ints(indices)
		for _, n := range indices {
			store.nextID++
			attachments = append(attachments, &discordgo.MessageAttachment{ID: strconv.Itoa(store.nextID), Filename: uploads[strconv.Itoa(n)]})
		}
		if len(attachments) > 10 {
			t.Fatalf("Discord message has %d attachments, maximum 10", len(attachments))
		}
		content := ""
		if edit.Content != nil {
			content = *edit.Content
		}
		store.messages[id] = &discordgo.Message{ID: id, ChannelID: "chan", Content: content, Attachments: attachments}
		return archiveMessageResponse(t, id, attachments)
	})
	return s, store
}

func archiveTestPages(start, count int) []ScreenshotPage {
	pages := make([]ScreenshotPage, count)
	for i := range pages {
		pages[i] = ScreenshotPage{Bytes: []byte(fmt.Sprintf("screenshot %d", start+i)), Names: []string{fmt.Sprintf("member%d", start+i)}}
	}
	return pages
}

func loadArchivePartForTest(t *testing.T, dbc *sql.DB, part int) (string, []storedArchivePage) {
	t.Helper()
	_, id, pages, err := loadGuildScreenshotArchive(dbc, arcGuildA, arcWeek, part)
	if err != nil {
		t.Fatal(err)
	}
	return id, pages
}

func TestScreenshotArchiveTwentyPages(t *testing.T) {
	dbc := testdb.TestDB(t)
	s, store := archivePartsSession(t)
	if err := upsertGuildScreenshotArchive(s, dbc, arcGuildA, "chan", arcWeek, archiveTestPages(0, 20)); err != nil {
		t.Fatal(err)
	}
	if len(store.messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(store.messages))
	}
	for part := 0; part < 2; part++ {
		id, pages := loadArchivePartForTest(t, dbc, part)
		if len(pages) != 10 || len(store.messages[id].Attachments) != 10 {
			t.Fatalf("part %d: %d pages", part, len(pages))
		}
		for i, page := range pages {
			want := fmt.Sprintf("member%d", part*10+i)
			if !reflect.DeepEqual(page.names, []string{want}) {
				t.Fatalf("part %d page %d = %v, want %s", part, i, page.names, want)
			}
		}
		if !strings.Contains(store.messages[id].Content, fmt.Sprintf("Part %d", part+1)) {
			t.Fatalf("part label missing: %q", store.messages[id].Content)
		}
	}
}

func TestScreenshotArchiveReplacementAcrossParts(t *testing.T) {
	dbc := testdb.TestDB(t)
	s, store := archivePartsSession(t)
	if err := upsertGuildScreenshotArchive(s, dbc, arcGuildA, "chan", arcWeek, archiveTestPages(0, 20)); err != nil {
		t.Fatal(err)
	}
	firstID, firstBefore := loadArchivePartForTest(t, dbc, 0)
	secondID, secondBefore := loadArchivePartForTest(t, dbc, 1)
	// A reverse-order partial batch must match globally and retain each page's
	// existing message, with no screenshots moving between parts.
	if err := upsertGuildScreenshotArchive(s, dbc, arcGuildA, "chan", arcWeek, []ScreenshotPage{archiveTestPages(15, 1)[0], archiveTestPages(5, 1)[0]}); err != nil {
		t.Fatal(err)
	}
	for part, before := range [][]storedArchivePage{firstBefore, secondBefore} {
		id, after := loadArchivePartForTest(t, dbc, part)
		if id != []string{firstID, secondID}[part] || len(after) != 10 {
			t.Fatalf("part %d changed message/count", part)
		}
		byID := make(map[int64]storedArchivePage)
		for _, page := range after {
			byID[page.id] = page
		}
		for i, page := range before {
			if i == 5 {
				if byID[page.id].attachmentID == page.attachmentID {
					t.Fatalf("part %d replacement kept old image", part)
				}
			} else if !reflect.DeepEqual(byID[page.id], page) {
				t.Fatalf("part %d survivor changed: %+v", part, page)
			}
		}
	}
	if len(store.messages) != 2 {
		t.Fatal("partial replacement created an extra message")
	}
}

func TestScreenshotArchiveSecondPartFailurePreservesReferences(t *testing.T) {
	dbc := testdb.TestDB(t)
	s, store := archivePartsSession(t)
	if err := upsertGuildScreenshotArchive(s, dbc, arcGuildA, "chan", arcWeek, archiveTestPages(0, 20)); err != nil {
		t.Fatal(err)
	}
	_, firstBefore := loadArchivePartForTest(t, dbc, 0)
	secondID, secondBefore := loadArchivePartForTest(t, dbc, 1)
	store.failMessage = secondID
	if err := upsertGuildScreenshotArchive(s, dbc, arcGuildA, "chan", arcWeek, archiveTestPages(0, 20)); err == nil {
		t.Fatal("second-part failure was swallowed")
	}
	_, firstAfter := loadArchivePartForTest(t, dbc, 0)
	_, secondAfter := loadArchivePartForTest(t, dbc, 1)
	if reflect.DeepEqual(firstBefore, firstAfter) {
		t.Fatal("successful first part was not saved")
	}
	if !reflect.DeepEqual(secondBefore, secondAfter) {
		t.Fatal("failed second part lost its references")
	}
}

func TestScreenshotArchiveDeletedSecondPartRecreated(t *testing.T) {
	dbc := testdb.TestDB(t)
	s, store := archivePartsSession(t)
	if err := upsertGuildScreenshotArchive(s, dbc, arcGuildA, "chan", arcWeek, archiveTestPages(0, 20)); err != nil {
		t.Fatal(err)
	}
	firstID, firstBefore := loadArchivePartForTest(t, dbc, 0)
	secondID, _ := loadArchivePartForTest(t, dbc, 1)
	delete(store.messages, secondID)
	if err := upsertGuildScreenshotArchive(s, dbc, arcGuildA, "", arcWeek, archiveTestPages(10, 10)); err != nil {
		t.Fatal(err)
	}
	firstAfterID, firstAfter := loadArchivePartForTest(t, dbc, 0)
	secondAfterID, secondAfter := loadArchivePartForTest(t, dbc, 1)
	if firstID != firstAfterID || !reflect.DeepEqual(firstBefore, firstAfter) {
		t.Fatal("recreating part 2 changed part 1")
	}
	if secondID == secondAfterID || len(secondAfter) != 10 {
		t.Fatalf("second part was not recreated: %s %d", secondAfterID, len(secondAfter))
	}
}

func TestScreenshotArchiveTwentyPageRetention(t *testing.T) {
	dbc := testdb.TestDB(t)
	s, store := archivePartsSession(t)
	if err := upsertGuildScreenshotArchive(s, dbc, arcGuildA, "chan", arcWeek, archiveTestPages(0, 20)); err != nil {
		t.Fatal(err)
	}
	// Ten entirely new screenshots replace the oldest unmatched screenshots,
	// even though the incoming batch has no name matches.
	if err := upsertGuildScreenshotArchive(s, dbc, arcGuildA, "chan", arcWeek, archiveTestPages(20, 10)); err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool)
	for part := 0; part < 2; part++ {
		id, pages := loadArchivePartForTest(t, dbc, part)
		if len(pages) != 10 || len(store.messages[id].Attachments) != 10 {
			t.Fatalf("part %d count wrong", part)
		}
		for _, page := range pages {
			names[page.names[0]] = true
		}
	}
	for i := 0; i < 30; i++ {
		if names[fmt.Sprintf("member%d", i)] != (i >= 10) {
			t.Fatalf("incorrect retention for member%d: %v", i, names)
		}
	}
}

func TestClearScreenshotArchiveBothParts(t *testing.T) {
	for _, failSecond := range []bool{false, true} {
		t.Run(fmt.Sprintf("second_part_failure=%v", failSecond), func(t *testing.T) {
			dbc := testdb.TestDB(t)
			t.Setenv(data.EnvVarDiscordGuildID, "")
			s, store := archivePartsSession(t)
			if err := upsertGuildScreenshotArchive(s, dbc, arcGuildA, "chan", arcWeek, archiveTestPages(0, 20)); err != nil {
				t.Fatal(err)
			}
			secondID, secondBefore := loadArchivePartForTest(t, dbc, 1)
			if failSecond {
				store.failMessage = secondID
			}
			week, _ := time.Parse(time.DateOnly, arcWeek)
			err := ClearWeekScreenshots(s, dbc, arcGuildA, week)
			if (err != nil) != failSecond {
				t.Fatalf("clear error = %v", err)
			}
			for part := 0; part < 2; part++ {
				id, after := loadArchivePartForTest(t, dbc, part)
				if failSecond && part == 1 {
					if !reflect.DeepEqual(after, secondBefore) {
						t.Fatal("failed part's references were cleared")
					}
				} else if len(after) != 0 || len(store.messages[id].Attachments) != 0 {
					t.Fatalf("part %d was not cleared", part)
				}
			}
		})
	}
}

func TestScreenshotArchiveGrowsIntoSecondPart(t *testing.T) {
	dbc := testdb.TestDB(t)
	s, store := archivePartsSession(t)
	if err := upsertGuildScreenshotArchive(s, dbc, arcGuildA, "chan", arcWeek, archiveTestPages(0, 10)); err != nil {
		t.Fatal(err)
	}
	firstID, firstBefore := loadArchivePartForTest(t, dbc, 0)
	// A later submission fills the overflow part without editing, moving, or
	// discarding the ten screenshots already in the first message.
	if err := upsertGuildScreenshotArchive(s, dbc, arcGuildA, "", arcWeek, archiveTestPages(10, 10)); err != nil {
		t.Fatal(err)
	}
	if err := upsertGuildScreenshotArchive(s, dbc, arcGuildA, "", arcWeek, archiveTestPages(15, 1)); err != nil {
		t.Fatal(err)
	}
	firstAfterID, firstAfter := loadArchivePartForTest(t, dbc, 0)
	secondID, secondAfter := loadArchivePartForTest(t, dbc, 1)
	if firstID != firstAfterID || !reflect.DeepEqual(firstBefore, firstAfter) {
		t.Fatal("overflow submission changed the first part")
	}
	if len(secondAfter) != 10 || len(store.messages[secondID].Attachments) != 10 {
		t.Fatal("overflow lost screenshots")
	}
	if len(store.messages) != 2 {
		t.Fatalf("got %d messages", len(store.messages))
	}
}

func TestScreenshotArchiveRejectsOversizedBatch(t *testing.T) {
	dbc := testdb.TestDB(t)
	s, store := archivePartsSession(t)
	if err := upsertGuildScreenshotArchive(s, dbc, arcGuildA, "chan", arcWeek, archiveTestPages(0, 21)); err == nil {
		t.Fatal("oversized batch silently accepted")
	}
	if len(store.requests) != 0 || archiveRowExists(dbc, arcGuildA, arcWeek) {
		t.Fatal("oversized batch partially archived")
	}
}
