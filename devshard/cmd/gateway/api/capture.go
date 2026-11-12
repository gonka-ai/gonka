package api

import (
	"cmp"
	"encoding/base64"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	json "github.com/goccy/go-json"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/internal/strictjson"
)

const (
	captureDirName         = "captured-requests"
	captureFileNameLayout  = "20060102T150405.000000000Z"
	captureDirPermissions  = 0o700
	captureFilePermissions = 0o600
)

type capturedRequest struct {
	RequestID  string          `json:"request_id,omitempty"`
	CapturedAt string          `json:"captured_at"`
	Kind       string          `json:"kind"`
	Error      string          `json:"error,omitempty"`
	Method     string          `json:"method,omitempty"`
	Path       string          `json:"path,omitempty"`
	Model      string          `json:"model,omitempty"`
	Escrow     string          `json:"escrow,omitempty"`
	Stream     bool            `json:"stream,omitempty"`
	Body       json.RawMessage `json:"body,omitempty"`
	BodyBase64 string          `json:"body_base64,omitempty"`
}

type requestCapture struct {
	dir        string
	sampleRate float64
	maxBytes   int64
	now        func() time.Time
	seen       atomic.Int64
	sequence   atomic.Int64
	written    atomic.Int64
	held       atomic.Int64
	refused    atomic.Int64
	failed     atomic.Int64
}

func newRequestCapture(settings config.Capture, storageDir string, now func() time.Time) (*requestCapture, error) {
	if !settings.Enabled {
		return nil, nil
	}
	dir := strings.TrimSpace(settings.Dir)
	if dir == "" {
		dir = filepath.Join(storageDir, captureDirName)
	}
	held, err := directoryBytes(dir)
	if err != nil {
		return nil, fmt.Errorf("api: measuring capture directory %s: %w", dir, err)
	}
	capture := &requestCapture{dir: dir, sampleRate: settings.SampleRate, maxBytes: settings.MaxBytes, now: now}
	capture.held.Store(held)
	return capture, nil
}

func directoryBytes(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	return total, err
}

func (c *requestCapture) filterRejected(r *http.Request, requestID string, body []byte, cause error) {
	if c == nil {
		return
	}
	model, stream := chatRequestHints(body)
	c.record(captureFilterRejected, r, requestID, model, stream, body, cause)
}

func (c *requestCapture) attemptsFailed(r *http.Request, requestID string, normalized filters.Result, cause error) {
	if c == nil {
		return
	}
	c.record(captureAttemptsFailed, r, requestID, normalized.Model, normalized.ClientStream, normalized.Body, cause)
}

func (c *requestCapture) record(kind string, r *http.Request, requestID, model string, stream bool, body []byte, cause error) {
	if cause == nil || len(body) == 0 || !c.admit() {
		return
	}
	record := capturedRequest{
		RequestID:  requestID,
		CapturedAt: c.now().UTC().Format(time.RFC3339Nano),
		Kind:       kind,
		Error:      cause.Error(),
		Method:     r.Method,
		Path:       r.URL.Path,
		Model:      model,
		Escrow:     r.PathValue("id"),
		Stream:     stream,
	}
	if stdjson.Valid(body) {
		record.Body = json.RawMessage(body)
	} else {
		record.BodyBase64 = base64.StdEncoding.EncodeToString(body)
	}
	if err := c.write(record); err != nil {
		c.failed.Add(1)
	}
}

// admit passes a deterministic stride, not a random sample. See README.md, "Request capture".
func (c *requestCapture) admit() bool {
	switch {
	case c.sampleRate >= 1:
		return true
	case c.sampleRate <= 0:
		return false
	}
	seen := float64(c.seen.Add(1))
	return math.Floor(seen*c.sampleRate) > math.Floor((seen-1)*c.sampleRate)
}

func (c *requestCapture) write(record capturedRequest) error {
	payload, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	size := int64(len(payload))
	if c.held.Add(size) > c.maxBytes {
		c.held.Add(-size)
		c.refused.Add(1)
		return nil
	}

	dir := filepath.Join(c.dir, record.Kind)
	name := fmt.Sprintf("%s_%09d_%s_%s.json",
		c.now().UTC().Format(captureFileNameLayout),
		c.sequence.Add(1),
		safeFileNameComponent(cmp.Or(record.RequestID, "no-request-id")),
		record.Kind,
	)
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(dir, captureDirPermissions); err != nil {
		c.held.Add(-size)
		return err
	}
	if err := os.WriteFile(path+".tmp", payload, captureFilePermissions); err != nil {
		c.held.Add(-size)
		return err
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		c.held.Add(-size)
		return err
	}
	c.written.Add(1)
	return nil
}

func safeFileNameComponent(value string) string {
	var cleaned strings.Builder
	for _, letter := range strings.TrimSpace(value) {
		switch {
		case letter >= 'a' && letter <= 'z', letter >= 'A' && letter <= 'Z', letter >= '0' && letter <= '9':
			cleaned.WriteRune(letter)
		case letter == '-', letter == '_', letter == '.':
			cleaned.WriteRune(letter)
		default:
			cleaned.WriteByte('_')
		}
	}
	return strings.Trim(cleaned.String(), "._")
}

func chatRequestHints(body []byte) (model string, stream bool) {
	var request struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := strictjson.Unmarshal(body, &request); err != nil {
		return "", false
	}
	return request.Model, request.Stream
}

// stats is what the sink can say about itself; refused and failed differ. See README.md, "Request capture".
func (c *requestCapture) stats() (written, refused, failed, held int64) {
	if c == nil {
		return 0, 0, 0, 0
	}
	return c.written.Load(), c.refused.Load(), c.failed.Load(), c.held.Load()
}
