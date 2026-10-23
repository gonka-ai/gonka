package dapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"trainshard/internal/domain/shared/vo"
)

type fakeDapi struct {
	enabled, stopped bool
	locks            int
	status           string
	failing          map[string]bool
	calls            []string
}

func (d *fakeDapi) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodGet && r.URL.Path == "/admin/v1/nodes" {
		body, _ := json.Marshal([]map[string]any{{
			"node":  map[string]any{"id": "node1"},
			"state": map[string]any{"current_status": d.status, "lock_count": d.locks, "admin_state": map[string]any{"enabled": d.enabled, "epoch": 0, "stopped": d.stopped}},
		}})
		return answer(http.StatusOK, body), nil
	}
	action, isAction := strings.CutPrefix(r.URL.Path, "/admin/v1/nodes/node1/")
	if r.Method != http.MethodPost || !isAction {
		return answer(http.StatusNotFound, nil), nil
	}
	d.calls = append(d.calls, action)
	if d.failing[action] {
		return answer(http.StatusServiceUnavailable, nil), nil
	}
	switch action {
	case "disable":
		d.enabled = false
	case "enable":
		d.enabled = true
	case "stop":
		d.stopped, d.status = true, "STOPPED"
	case "start":
		d.stopped, d.status = false, "INFERENCE"
	default:
		return answer(http.StatusNotFound, nil), nil
	}
	return answer(http.StatusOK, []byte(`{"message":"ok"}`)), nil
}

func answer(status int, body []byte) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(body))), Header: http.Header{}}
}

func newClient(d *fakeDapi) *Client {
	return New(&http.Client{Transport: d}, Config{Address: "http://dapi", Timeout: time.Second})
}

func TestDrainAndReturnRoundTrip(t *testing.T) {
	// arrange
	d := &fakeDapi{enabled: true, status: "INFERENCE"}
	client := newClient(d)
	ref := vo.NodeRef{NodeID: "node1"}

	// act
	drained, drainErr := client.Drain(context.Background(), ref)
	returnErr := client.Return(context.Background(), ref)
	back, backErr := client.Drained(context.Background(), ref)

	// assert
	if err := errors.Join(drainErr, returnErr, backErr); err != nil {
		t.Fatal(err)
	}
	if !drained {
		t.Fatal("drain should report the node drained once the dapi stopped it")
	}
	if back {
		t.Fatal("a returned node must not read as drained")
	}
	if got := strings.Join(d.calls, ","); got != "disable,stop,start,enable" {
		t.Fatalf("calls = %s", got)
	}
}

func TestDrainIsNotDoneWhileInferenceIsStillOnTheNode(t *testing.T) {
	// arrange
	d := &fakeDapi{enabled: true, locks: 2, status: "INFERENCE"}
	client := newClient(d)

	// act
	drained, err := client.Drain(context.Background(), vo.NodeRef{NodeID: "node1"})

	// assert
	if err != nil {
		t.Fatal(err)
	}
	if drained {
		t.Fatal("a node still answering inference is not drained")
	}
}

func TestDrainAndReturnOnlySendWhatIsMissing(t *testing.T) {
	cases := []struct {
		name             string
		enabled, stopped bool
		status           string
		act              func(*Client, vo.NodeRef) (bool, error)
		want             string
		wantDrained      bool
	}{
		{"drain of a node already taken out", false, true, "STOPPED", drain, "", true},
		{"drain of a node told to stop that has not got there yet", false, true, "INFERENCE", drain, "", false},
		{"drain of a node disabled but still serving", false, false, "INFERENCE", drain, "stop", true},
		{"return of a node in service", true, false, "INFERENCE", ret, "", false},
		{"return of a node started but not enabled", false, false, "INFERENCE", ret, "enable", false},
		{"return of a node enabled but not started", true, true, "STOPPED", ret, "start", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// arrange
			d := &fakeDapi{enabled: tc.enabled, stopped: tc.stopped, status: tc.status}

			// act
			drained, err := tc.act(newClient(d), vo.NodeRef{NodeID: "node1"})

			// assert
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(d.calls, ","); got != tc.want {
				t.Fatalf("calls = %q, want %q", got, tc.want)
			}
			if drained != tc.wantDrained {
				t.Fatalf("drained = %v, want %v", drained, tc.wantDrained)
			}
		})
	}
}

func drain(c *Client, ref vo.NodeRef) (bool, error) { return c.Drain(context.Background(), ref) }

func ret(c *Client, ref vo.NodeRef) (bool, error) { return false, c.Return(context.Background(), ref) }

func TestReturnReportsAFailedStepSoItIsAskedAgain(t *testing.T) {
	// arrange
	d := &fakeDapi{stopped: true, status: "STOPPED", failing: map[string]bool{"enable": true}}
	client := newClient(d)
	ref := vo.NodeRef{NodeID: "node1"}

	// act
	first := client.Return(context.Background(), ref)
	d.failing = nil
	second := client.Return(context.Background(), ref)

	// assert
	if first == nil {
		t.Fatal("a failed enable must be reported so the return is asked for again")
	}
	if second != nil {
		t.Fatal(second)
	}
	if got := strings.Join(d.calls, ","); got != "start,enable,enable" {
		t.Fatalf("calls = %s, want the second return to finish only what the first left undone", got)
	}
}

func TestDrained(t *testing.T) {
	held := func(enabled, stopped bool, locks int, status string) node {
		var n node
		n.State.AdminState.Enabled = enabled
		n.State.AdminState.Stopped = stopped
		n.State.LockCount = locks
		n.State.CurrentStatus = status
		return n
	}
	cases := []struct {
		name string
		held node
		want bool
	}{
		{"disabled, stopped, idle and the mlnode reports stopped", held(false, true, 0, "STOPPED"), true},
		{"disabled but the mlnode still serves", held(false, false, 0, "INFERENCE"), false},
		{"stop asked for but the mlnode has not reached it", held(false, true, 0, "INFERENCE"), false},
		{"stopped but still enabled", held(true, true, 0, "STOPPED"), false},
		{"stopped but still holding work", held(false, true, 1, "STOPPED"), false},
		{"in the middle of a proof of compute", held(false, true, 0, "POC"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// act
			got := drained(tc.held)

			// assert
			if got != tc.want {
				t.Fatalf("drained = %v, want %v", got, tc.want)
			}
		})
	}
}
