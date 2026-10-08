package hosts

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/utils/timex"
)

type signerStub struct{}

func (signerStub) Sign([]byte) []byte { return []byte("signature") }

func TestAnAnswerAHostCannotBackIsRefused(t *testing.T) {
	cases := []struct {
		name   string
		answer string
	}{
		{"an envelope past the answer limit", `{"ok":true,"data":{"items":[]},"meta":{"request_id":"` + strings.Repeat("x", maxAnswerBytes) + `"}}`},
		{"data that is not the result asked for", `{"ok":true,"data":{"items":"none"},"meta":{"request_id":"req-1"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// arrange
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.answer))
			}))
			t.Cleanup(server.Close)
			clock := timex.NewFrozen(time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))
			client := New(server.Client(), signerStub{}, clock, time.Minute)
			host := vo.Host{Participant: "gonka1host", Endpoint: vo.Endpoint(server.URL)}
			call := run.HostCommand{Shard: 7, RequestID: "req-1", Deadline: clock.Now().Add(time.Minute)}

			// act
			_, err := client.Status(context.Background(), host, call)

			// assert
			if !errors.Is(err, shared.ErrUnavailable) || shared.CodeOf(err) != "HOST_ANSWER" {
				t.Fatalf("got %v (%s), want HOST_ANSWER", err, shared.CodeOf(err))
			}
		})
	}
}

func TestStopOmitsGraceOnTheWireWhenTheCallerDidNotSetIt(t *testing.T) {
	// arrange
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"ok":true,"data":{"items":[]},"meta":{"request_id":"req-1"}}`))
	}))
	t.Cleanup(server.Close)
	clock := timex.NewFrozen(time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))
	client := New(server.Client(), signerStub{}, clock, time.Minute)
	node := vo.NodeRef{Participant: "gonka1host", NodeID: "node-1"}
	host := vo.Host{Participant: node.Participant, Endpoint: vo.Endpoint(server.URL), Nodes: []vo.NodeRef{node}}
	call := run.StopCall{HostCommand: run.HostCommand{Shard: 7, Nodes: host.Nodes, RequestID: "req-1", Deadline: clock.Now().Add(time.Minute)}}

	// act
	_, err := client.Stop(context.Background(), host, call)

	// assert
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	var sent map[string]any
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if _, found := sent["grace_seconds"]; found {
		t.Fatalf("got %s, want grace_seconds omitted", body)
	}
}

func TestStatusKeepsOnlyThePeersThatAreNodeRefs(t *testing.T) {
	// arrange
	answer := `{"ok":true,"data":{"items":[{"node_id":"node-1","state":"running","mesh_up":true,` +
		`"mesh_silent":["gonka1hostb/node-2","gonka1\u001b[2Jhost/node-3","gonka1hostc/../x","no-slash"]}]},` +
		`"meta":{"request_id":"req-1"}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(server.Close)
	clock := timex.NewFrozen(time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))
	client := New(server.Client(), signerStub{}, clock, time.Minute)
	node := vo.NodeRef{Participant: "gonka1host", NodeID: "node-1"}
	host := vo.Host{Participant: node.Participant, Endpoint: vo.Endpoint(server.URL), Nodes: []vo.NodeRef{node}}
	call := run.HostCommand{Shard: 7, Nodes: host.Nodes, RequestID: "req-1", Deadline: clock.Now().Add(time.Minute)}

	// act
	statuses, err := client.Status(context.Background(), host, call)

	// assert
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	want := []vo.NodeRef{{Participant: "gonka1hostb", NodeID: "node-2"}}
	if len(statuses) != 1 || !slices.Equal(statuses[0].MeshSilent, want) {
		t.Fatalf("got %+v, want only gonka1hostb/node-2", statuses)
	}
}

func TestStopSendsExplicitZeroGraceOnTheWire(t *testing.T) {
	// arrange
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"ok":true,"data":{"items":[]},"meta":{"request_id":"req-1"}}`))
	}))
	t.Cleanup(server.Close)
	clock := timex.NewFrozen(time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))
	client := New(server.Client(), signerStub{}, clock, time.Minute)
	node := vo.NodeRef{Participant: "gonka1host", NodeID: "node-1"}
	host := vo.Host{Participant: node.Participant, Endpoint: vo.Endpoint(server.URL), Nodes: []vo.NodeRef{node}}
	call := run.StopCall{
		HostCommand: run.HostCommand{Shard: 7, Nodes: host.Nodes, RequestID: "req-1", Deadline: clock.Now().Add(time.Minute)},
		Grace:       0,
		GraceGiven:  true,
	}

	// act
	_, err := client.Stop(context.Background(), host, call)

	// assert
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	var sent map[string]any
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if value, found := sent["grace_seconds"]; !found || value != float64(0) {
		t.Fatalf("got %s, want grace_seconds set to 0", body)
	}
}

func TestStopRoundsAPartOfASecondUp(t *testing.T) {
	// arrange
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"ok":true,"data":{"items":[]},"meta":{"request_id":"req-1"}}`))
	}))
	t.Cleanup(server.Close)
	clock := timex.NewFrozen(time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))
	client := New(server.Client(), signerStub{}, clock, time.Minute)
	node := vo.NodeRef{Participant: "gonka1host", NodeID: "node-1"}
	host := vo.Host{Participant: node.Participant, Endpoint: vo.Endpoint(server.URL), Nodes: []vo.NodeRef{node}}
	call := run.StopCall{
		HostCommand: run.HostCommand{Shard: 7, Nodes: host.Nodes, RequestID: "req-1", Deadline: clock.Now().Add(time.Minute)},
		Grace:       1500 * time.Millisecond,
		GraceGiven:  true,
	}

	// act
	_, err := client.Stop(context.Background(), host, call)

	// assert
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	var sent map[string]any
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if value := sent["grace_seconds"]; value != float64(2) {
		t.Fatalf("got %s, want grace_seconds rounded up to 2", body)
	}
}
