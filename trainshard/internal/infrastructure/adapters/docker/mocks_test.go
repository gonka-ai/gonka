package docker

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

type answer struct {
	status int
	body   any
	err    error
}

type call struct {
	method    string
	path      string
	body      []byte
	remaining time.Duration
}

type engineStub struct {
	route func(method, path string) answer
	calls []call
}

func (e *engineStub) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	recorded := call{method: r.Method, path: r.URL.Path, body: body}
	if deadline, found := r.Context().Deadline(); found {
		recorded.remaining = time.Until(deadline)
	}
	e.calls = append(e.calls, recorded)

	given := e.route(r.Method, r.URL.Path)
	if given.err != nil {
		return nil, given.err
	}
	raw, err := json.Marshal(given.body)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: given.status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(raw)),
		Request:    r,
	}, nil
}

func (e *engineStub) sent(method, suffix string) (call, bool) {
	for _, recorded := range e.calls {
		if recorded.method == method && strings.HasSuffix(recorded.path, suffix) {
			return recorded, true
		}
	}
	return call{}, false
}

func stubbed(t *testing.T, cfg Config, route func(method, path string) answer) (*Client, *engineStub) {
	t.Helper()

	stub := &engineStub{route: route}
	engine, err := client.New(client.WithAPIVersion("1.47"), client.WithHTTPClient(&http.Client{Transport: stub}))
	if err != nil {
		t.Fatal(err)
	}
	return &Client{cfg: cfg.withDefaults(), log: slog.New(slog.DiscardHandler), engine: engine}, stub
}

var missing = answer{status: http.StatusNotFound, body: map[string]string{"message": "no such object"}}
