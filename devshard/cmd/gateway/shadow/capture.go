package shadow

import (
	"bytes"
	"strings"
	"sync"
	"time"

	json "github.com/goccy/go-json"
)

type capture struct {
	now     func() time.Time
	mu      sync.Mutex
	started time.Time
	first   time.Time
	seen    bool
	body    []byte
}

func newCapture(now func() time.Time) *capture {
	return &capture{now: now, started: now()}
}

func (c *capture) Sent() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.started = c.now()
}

func (c *capture) Write(chunk []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.seen {
		c.seen, c.first = true, c.now()
	}
	c.body = append(c.body, chunk...)
	if excess := len(c.body) - maxCaptureBytes; excess > 0 {
		c.body = c.body[:copy(c.body, c.body[excess:])]
	}
	return len(chunk), nil
}

func (c *capture) firstToken() (elapsed time.Duration, streamed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.first.Sub(c.started), c.seen
}

type usageEvent struct {
	Usage *struct {
		CompletionTokens uint64 `json:"completion_tokens"`
	} `json:"usage"`
}

func (c *capture) completionTokens() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var tokens uint64
	for line := range bytes.Lines(c.body) {
		payload, isData := strings.CutPrefix(strings.TrimSpace(string(line)), dataPrefix)
		payload = strings.TrimSpace(payload)
		if !isData || payload == doneMarker {
			continue
		}
		var event usageEvent
		if json.Unmarshal([]byte(payload), &event) == nil && event.Usage != nil {
			tokens = event.Usage.CompletionTokens
		}
	}
	return tokens
}
