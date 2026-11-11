package imagesubmissions

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/tomerh2001/maple-culvert-tracker/internal/commands/helpers"
)

const MaxPages = 20
const MaxImageBytes = 8 << 20
const MaxBatchBytes = 64 << 20
const MaxManifestBytes = 32 << 10
const MaxRequestBytes = MaxBatchBytes + (64 << 10)

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var hexSHA = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Page struct {
	Ordinal       int       `json:"ordinal"`
	AcquisitionID string    `json:"acquisition_id"`
	CapturedAt    time.Time `json:"captured_at_utc"`
	SHA256        string    `json:"sha256"`
	ByteLength    int       `json:"byte_length"`
	Width         int       `json:"width"`
	Height        int       `json:"height"`
	PixelFormat   string    `json:"pixel_format"`
}
type Manifest struct {
	SchemaVersion     int       `json:"schema_version"`
	Origin            string    `json:"origin"`
	CaptureRunID      string    `json:"capture_run_id"`
	NativeOperationID string    `json:"native_operation_id"`
	CollectorID       string    `json:"collector_id"`
	DestinationID     string    `json:"destination_id"`
	MapleGuild        string    `json:"maple_guild"`
	ScheduleRevision  string    `json:"schedule_revision"`
	SlotID            string    `json:"slot_id"`
	ResetAt           time.Time `json:"reset_at_utc"`
	StartedAt         time.Time `json:"captured_started_at_utc"`
	EndedAt           time.Time `json:"captured_ended_at_utc"`
	Week              string    `json:"expected_week_key"`
	Complete          bool      `json:"complete"`
	FirstPage         bool      `json:"first_page_verified"`
	LastPage          bool      `json:"last_page_verified"`
	Pages             []Page    `json:"pages"`
}
type Batch struct {
	Manifest  Manifest
	Images    [][]byte
	Digest    string
	Canonical []byte
}
type APIError struct {
	Status int
	Code   string
}

func (e *APIError) Error() string { return e.Code }
func invalid(code string) error   { return &APIError{422, code} }

func strictJSON(b []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func validateManifest(m Manifest, p Principal, now time.Time) error {
	if m.SchemaVersion != 1 || m.Origin != "submit-images" || !m.Complete || !m.FirstPage || !m.LastPage {
		return invalid("incomplete_manifest")
	}
	for _, id := range []string{m.CaptureRunID, m.NativeOperationID, m.CollectorID, m.DestinationID, m.ScheduleRevision, m.SlotID} {
		if !safeID.MatchString(id) {
			return invalid("invalid_identifier")
		}
	}
	if m.CollectorID != p.CollectorID || m.DestinationID != p.DestinationID || m.MapleGuild != p.MapleGuild {
		return &APIError{403, "destination_mismatch"}
	}
	if len(m.Pages) < 1 || len(m.Pages) > MaxPages {
		return &APIError{413, "page_limit"}
	}
	if m.StartedAt.IsZero() || m.EndedAt.Before(m.StartedAt) || m.EndedAt.Sub(m.StartedAt) > 3*time.Minute || m.EndedAt.After(now.Add(2*time.Minute)) {
		return invalid("invalid_capture_interval")
	}
	nextReset := time.Date(m.StartedAt.UTC().Year(), m.StartedAt.UTC().Month(), m.StartedAt.UTC().Day()+1, 0, 0, 0, 0, time.UTC)
	if !m.ResetAt.Equal(nextReset) {
		return invalid("invalid_reset")
	}
	week := helpers.CurrentCulvertWeek(m.StartedAt).Format(time.DateOnly)
	if m.Week != week || helpers.CurrentCulvertWeek(m.EndedAt).Format(time.DateOnly) != week {
		return invalid("cross_reset_capture")
	}
	seen := map[string]bool{}
	prior := m.StartedAt
	total := 0
	for i, page := range m.Pages {
		if page.Ordinal != i+1 || !safeID.MatchString(page.AcquisitionID) || seen[page.AcquisitionID] {
			return invalid("invalid_page_sequence")
		}
		seen[page.AcquisitionID] = true
		if page.CapturedAt.Before(prior) || page.CapturedAt.After(m.EndedAt) {
			return invalid("invalid_page_time")
		}
		prior = page.CapturedAt
		if !hexSHA.MatchString(page.SHA256) {
			return invalid("invalid_image_hash")
		}
		if page.ByteLength < 1 || page.ByteLength > MaxImageBytes || page.Width < 1 || page.Height < 1 || page.Width > 8192 || page.Height > 8192 || int64(page.Width)*int64(page.Height) > 24_000_000 {
			return &APIError{413, "image_limit"}
		}
		if page.PixelFormat != "gray8" && page.PixelFormat != "rgb8" {
			return invalid("invalid_pixel_format")
		}
		total += page.ByteLength
	}
	if total > MaxBatchBytes {
		return &APIError{413, "batch_limit"}
	}
	return nil
}

func readBatch(w http.ResponseWriter, r *http.Request, p Principal, now time.Time) (*Batch, error) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, &APIError{400, "multipart_required"}
	}
	part, err := mr.NextPart()
	if err != nil || part.FormName() != "manifest" || part.FileName() != "" {
		return nil, &APIError{400, "manifest_required"}
	}
	raw, err := io.ReadAll(io.LimitReader(part, MaxManifestBytes+1))
	part.Close()
	if err != nil {
		return nil, &APIError{400, "manifest_read"}
	}
	if len(raw) > MaxManifestBytes {
		return nil, &APIError{413, "manifest_limit"}
	}
	b := &Batch{}
	if err := strictJSON(raw, &b.Manifest); err != nil {
		return nil, &APIError{400, "invalid_manifest"}
	}
	if err := validateManifest(b.Manifest, p, now); err != nil {
		return nil, err
	}
	if r.Header.Get("Idempotency-Key") != b.Manifest.CaptureRunID {
		return nil, &APIError{400, "idempotency_key_mismatch"}
	}
	for _, page := range b.Manifest.Pages {
		part, err = mr.NextPart()
		if err != nil {
			return nil, &APIError{400, "missing_image"}
		}
		want := "page-" + fmtOrdinal(page.Ordinal)
		contentType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if part.FormName() != want || contentType != "image/png" {
			part.Close()
			return nil, &APIError{400, "image_part_mismatch"}
		}
		raw, err = io.ReadAll(io.LimitReader(part, MaxImageBytes+1))
		part.Close()
		if err != nil {
			return nil, &APIError{400, "image_read"}
		}
		if len(raw) > MaxImageBytes {
			return nil, &APIError{413, "image_limit"}
		}
		if len(raw) != page.ByteLength {
			return nil, invalid("image_length_mismatch")
		}
		hash := sha256.Sum256(raw)
		if hex.EncodeToString(hash[:]) != page.SHA256 {
			return nil, invalid("image_hash_mismatch")
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
		if err != nil || format != "png" || cfg.Width != page.Width || cfg.Height != page.Height || len(raw) < 26 {
			return nil, invalid("image_dimensions_mismatch")
		}
		// PNG IHDR bit depth and colour type: never label RGB/alpha data grayscale.
		wantType := byte(0)
		if page.PixelFormat == "rgb8" {
			wantType = 2
		}
		if raw[24] != 8 || raw[25] != wantType {
			return nil, invalid("image_pixel_format_mismatch")
		}
		b.Images = append(b.Images, raw)
	}
	if _, err := mr.NextPart(); err != io.EOF {
		return nil, &APIError{400, "unexpected_part"}
	}
	b.Canonical, err = json.Marshal(b.Manifest)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(b.Canonical)
	b.Digest = hex.EncodeToString(h[:])
	return b, nil
}

func fmtOrdinal(n int) string { return strconv.Itoa(n) }
