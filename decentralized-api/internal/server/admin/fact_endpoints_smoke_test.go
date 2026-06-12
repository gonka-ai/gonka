//go:build factsmoke

package admin

// Live smoke for the read-only fact endpoints: starts the real admin echo
// server on a TCP port and drives it with the system curl, printing full
// HTTP transcripts (status line, headers, body) for the dev_notes log.
// Guarded by the `factsmoke` build tag so it never runs in normal suites:
//
//	go test -tags factsmoke -run TestFactEndpointsCurlSmoke -v ./internal/server/admin/

import (
	"decentralized-api/apiconfig"
	"decentralized-api/chainphase"
	"fmt"
	"net"
	"os/exec"
	"testing"
	"time"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
)

func TestFactEndpointsCurlSmoke(t *testing.T) {
	s, cm, _ := setupTestServer(t)

	// One node, three configured models, so the launch-plan response
	// exercises every branch:
	//   test-model   — in governance (mock) AND in PoC params → a plan
	//   legacy-model — in PoC params but NOT in governance     → skipped
	//   old-model    — NOT in current PoC params               → unsupported
	assert.NoError(t, cm.SetNodes([]apiconfig.InferenceNodeConfig{{
		Id:               "mlnode-1",
		Host:             "localhost",
		InferencePort:    8080,
		InferenceSegment: "/api/v1",
		PoCPort:          8081,
		PoCSegment:       "/api/v1",
		MaxConcurrent:    3,
		Models: map[string]apiconfig.ModelConfig{
			"test-model":   {Args: []string{"--max-model-len", "8192"}},
			"legacy-model": {},
			"old-model":    {},
		},
	}}))
	assert.NoError(t, cm.SetPoCParams(apiconfig.PoCParamsCache{
		Models: []apiconfig.PoCModelConfigCache{
			{ModelId: "test-model", SeqLen: 1024},
			{ModelId: "legacy-model", SeqLen: 1024},
		},
	}))

	// Push the next PoC far out (~16h at 6s blocks) so the manual-test gate
	// is open and the timing numbers are meaningful.
	s.phaseTracker.Update(
		chainphase.BlockInfo{Height: 1, Hash: "smoke"},
		&types.Epoch{Index: 100, PocStartBlockHeight: 10000},
		&types.EpochParams{},
		true,
		nil,
	)

	// Real TCP listener — curl talks to it over the wire, exactly like an
	// operator would against a live API node.
	const addr = "127.0.0.1:19123"
	go func() { _ = s.e.Start(addr) }()
	defer s.e.Close()
	for i := 0; ; i++ {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if i > 50 {
			t.Fatalf("server did not start on %s", addr)
		}
		time.Sleep(100 * time.Millisecond)
	}

	base := "http://" + addr
	curl := func(label, method, url string) {
		out, err := exec.Command("curl", "-s", "-i", "-X", method, url).CombinedOutput()
		assert.NoError(t, err)
		fmt.Printf("\n===== %s =====\n$ curl -s -i -X %s %s\n%s\n", label, method, url, out)
	}

	curl("A. PoC timing (chain synced)", "GET", base+"/admin/v1/poc/timing")
	curl("B. Launch plan (plan + skipped + unsupported)", "GET", base+"/admin/v1/nodes/mlnode-1/launch-plan")
	curl("C. Last test result (no test recorded yet)", "GET", base+"/admin/v1/nodes/mlnode-1/test")
	curl("D. Run a test (POST, against the mock MLnode)", "POST", base+"/admin/v1/nodes/mlnode-1/test")
	curl("E. Last test result (raw result read back)", "GET", base+"/admin/v1/nodes/mlnode-1/test")
	curl("F. Unknown node launch-plan (404)", "GET", base+"/admin/v1/nodes/no-such-node/launch-plan")
	curl("G. Unknown node test result (404)", "GET", base+"/admin/v1/nodes/no-such-node/test")

	// Flip the tracker to not-synced: timing must report available=false
	// instead of zeros that would look like an imminent PoC.
	s.phaseTracker.Update(
		chainphase.BlockInfo{Height: 2, Hash: "smoke-2"},
		&types.Epoch{Index: 100, PocStartBlockHeight: 10000},
		&types.EpochParams{},
		false,
		nil,
	)
	curl("H. PoC timing (chain NOT synced)", "GET", base+"/admin/v1/poc/timing")
}
