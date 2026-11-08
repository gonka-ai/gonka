package loadtest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeAdminGateway serves the two admin routes the runner uses to add escrows: create one, list them.
type fakeAdminGateway struct {
	mu      sync.Mutex
	ids     []string
	created []map[string]any
	refuse  bool
	hide    bool // created escrows never show up in the list
}

func (g *fakeAdminGateway) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/admin/escrows", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "Bearer admin-key", r.Header.Get("Authorization"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.refuse {
			http.Error(w, `{"error":{"message":"chain unavailable"}}`, http.StatusBadGateway)
			return
		}
		g.created = append(g.created, body)
		id := uint64(100 + len(g.created))
		if !g.hide {
			g.ids = append(g.ids, fmt.Sprint(id))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"escrow_id": id, "registered": true})
	})
	mux.HandleFunc("/v1/admin/devshards", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		// A live escrow is listed active, as the gateway lists it.
		type devshard struct {
			ID     string `json:"id"`
			Active bool   `json:"active"`
		}
		var out struct {
			Devshards []devshard `json:"devshards"`
		}
		for _, id := range g.ids {
			out.Devshards = append(out.Devshards, devshard{ID: id, Active: true})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	return mux
}

// Test flow:
//  1. A gateway holds the configured escrow 1; the scenario asks for three escrows of its model.
//  2. Add the escrows.
//  3. Check two are created through the admin API with the scenario's model and amount and registered,
//     the runner waits until the gateway lists all three, and returns the new ids.
func TestAddScenarioEscrowsCreatesTheMissingOnesAndWaitsForTheGateway(t *testing.T) {
	g := &fakeAdminGateway{ids: []string{"1"}}
	ts := httptest.NewServer(g.handler(t))
	defer ts.Close()

	ids, err := addScenarioEscrows(context.Background(), ts.URL, "admin-key", "test-model", 1_000_000_000, 3, 5*time.Second)
	require.NoError(t, err)
	require.Equal(t, []string{"101", "102"}, ids)
	require.Len(t, g.created, 2)
	for _, body := range g.created {
		require.Equal(t, "test-model", body["model_id"])
		require.EqualValues(t, 1_000_000_000, body["amount"])
		require.Equal(t, true, body["register"])
	}

	ids, err = addScenarioEscrows(context.Background(), ts.URL, "admin-key", "test-model", 1_000_000_000, 1, time.Second)
	require.NoError(t, err)
	require.Empty(t, ids, "one escrow is the configured one")
	require.Len(t, g.created, 2)
}

// Test flow:
// 1. A gateway refuses to create an escrow, and another creates escrows it never lists.
// 2. Add escrows on each.
// 3. Check both fail, with the refusal and with the wait that ran out, instead of loading fewer escrows.
func TestAddScenarioEscrowsFailsWhenTheGatewayDoesNotHoldThem(t *testing.T) {
	refusing := &fakeAdminGateway{ids: []string{"1"}, refuse: true}
	ts := httptest.NewServer(refusing.handler(t))
	defer ts.Close()
	_, err := addScenarioEscrows(context.Background(), ts.URL, "admin-key", "test-model", 1, 2, time.Second)
	require.ErrorContains(t, err, "chain unavailable")

	hiding := &fakeAdminGateway{ids: []string{"1"}, hide: true}
	ts2 := httptest.NewServer(hiding.handler(t))
	defer ts2.Close()
	_, err = addScenarioEscrows(context.Background(), ts2.URL, "admin-key", "test-model", 1, 2, 1500*time.Millisecond)
	require.ErrorContains(t, err, "1 of 2 escrows")
}

func TestValidateBoundsTheEscrowCount(t *testing.T) {
	s := testScenario()
	s.Topology.Chain.Escrows = -1
	require.ErrorContains(t, s.Validate(), "topology.chain.escrows")
	s.Topology.Chain.Escrows = maxScenarioEscrows + 1
	require.ErrorContains(t, s.Validate(), "topology.chain.escrows")
	s.Topology.Chain.Escrows = 4
	require.NoError(t, s.Validate())
}

// The gateway on the network holds several escrows of a model and spreads the requests over them, so
// every scenario of the promise checks runs that way.
func TestPromiseScenariosRunSeveralEscrows(t *testing.T) {
	for _, name := range []string{"dishonest-executor-honest", "dishonest-executor-overclaim", "dishonest-executor-wrong-answer", "usage-challenge-wave", "usage-challenge-wave-honest", "chunk-shapes", "phase-faults", "drain", "shared-endpoint"} {
		s, err := LoadScenario(filepath.Join("scenarios", name+".yaml"))
		require.NoError(t, err, name)
		require.Equal(t, 4, s.Topology.Chain.Escrows, name)
	}
}

// Test flow:
//  1. A gateway holds escrows 1 and 2 and refuses the debug route at its root, as one with several
//     runtimes does; each escrow's scoped route lists one finished inference.
//  2. Wait for no orphaned work the way a scenario with four escrows asks.
//  3. Check the state is read from both escrows and nothing is orphaned.
func TestOrphanCheckReadsEveryEscrowOfARunWithSeveral(t *testing.T) {
	g := &fakeAdminGateway{ids: []string{"1", "2"}}
	mux := http.NewServeMux()
	mux.Handle("/v1/admin/", g.handler(t))
	mux.HandleFunc("/v1/debug/inferences", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "escrow required", http.StatusBadRequest)
	})
	for _, id := range g.ids {
		mux.HandleFunc("/devshard/"+id+"/v1/debug/inferences", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `{"inferences":{"1":{"status":"finished"}}}`)
		})
	}
	server := httptest.NewServer(mux)
	defer server.Close()

	scenario := testScenario()
	scenario.Topology.Chain.Escrows = 4
	require.True(t, scenario.severalRuntimes())
	_, summary, err := waitForNoOrphanedWork(context.Background(), server.URL, "admin-key", orphanJudge{Timeout: time.Second}, scenario.severalRuntimes())
	require.NoError(t, err)
	require.Equal(t, 2, summary.Finished)
	require.Zero(t, summary.Orphaned)
}
