package selfcheck

import (
	"decentralized-api/broker"
	"fmt"
	"strings"
	"time"
)

// CheckResult is the outcome of one assertion stage.
type CheckResult struct {
	Name    string `json:"name"`
	Pass    bool   `json:"pass"`
	Details string `json:"details,omitempty"`
}

// Report is the aggregate outcome of a selfcheck run. Pass is true iff
// every stage's Pass is true.
type Report struct {
	Pass   bool          `json:"pass"`
	Stages []CheckResult `json:"stages"`
}

// String renders the report as a human-readable summary suitable for
// printing to stderr at the end of a CLI selfcheck invocation.
func (r Report) String() string {
	var b strings.Builder
	if r.Pass {
		b.WriteString("selfcheck: PASS\n")
	} else {
		b.WriteString("selfcheck: FAIL\n")
	}
	for _, s := range r.Stages {
		mark := "PASS"
		if !s.Pass {
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "  [%s] %s", mark, s.Name)
		if s.Details != "" {
			fmt.Fprintf(&b, " — %s", s.Details)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Evaluator runs assertions against a broker driven through a
// synthetic PoC cycle by EventDriver. It is intentionally minimal:
// each stage queries broker state via the public GetNodes API and
// reports pass/fail. A full PoC artifact simulation is out of scope.
type Evaluator struct {
	Broker  *broker.Broker
	Bridge  *MockChainBridge
	NodeId  string
	Timeout time.Duration
}

// NewEvaluator constructs an Evaluator with a sensible default
// per-stage poll timeout.
func NewEvaluator(b *broker.Broker, bridge *MockChainBridge, nodeId string) *Evaluator {
	return &Evaluator{
		Broker:  b,
		Bridge:  bridge,
		NodeId:  nodeId,
		Timeout: 5 * time.Second,
	}
}

// AssertNodeRegistered waits until the broker reports the synthetic
// node as registered. Pass iff GetNodes returns an entry for NodeId
// within Timeout.
func (e *Evaluator) AssertNodeRegistered() CheckResult {
	deadline := time.Now().Add(e.Timeout)
	for time.Now().Before(deadline) {
		nodes, err := e.Broker.GetNodes()
		if err != nil {
			return CheckResult{Name: "node-registered", Pass: false, Details: err.Error()}
		}
		for _, n := range nodes {
			if n.Node.Id == e.NodeId {
				return CheckResult{Name: "node-registered", Pass: true}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return CheckResult{Name: "node-registered", Pass: false, Details: "timeout waiting for node"}
}

// AssertEpochModelsPopulated checks that after a chain refresh, the
// broker has populated EpochMLNodes for the synthetic node — i.e. the
// chain-bridge wiring is working end-to-end through the broker.
func (e *Evaluator) AssertEpochModelsPopulated() CheckResult {
	deadline := time.Now().Add(e.Timeout)
	for time.Now().Before(deadline) {
		nodes, err := e.Broker.GetNodes()
		if err != nil {
			return CheckResult{Name: "epoch-models-populated", Pass: false, Details: err.Error()}
		}
		for _, n := range nodes {
			if n.Node.Id != e.NodeId {
				continue
			}
			if len(n.State.EpochMLNodes) > 0 {
				return CheckResult{Name: "epoch-models-populated", Pass: true}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return CheckResult{Name: "epoch-models-populated", Pass: false, Details: "EpochMLNodes empty"}
}

// AssertHardwareDiffSubmitted checks that the broker reported its
// node hardware up to the (mocked) chain via SubmitHardwareDiff.
func (e *Evaluator) AssertHardwareDiffSubmitted() CheckResult {
	deadline := time.Now().Add(e.Timeout)
	for time.Now().Before(deadline) {
		if len(e.Bridge.SubmittedDiffsSnapshot()) > 0 {
			return CheckResult{Name: "hardware-diff-submitted", Pass: true}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return CheckResult{
		Name: "hardware-diff-submitted", Pass: false,
		Details: "broker did not submit hardware diff within timeout",
	}
}

// Combine aggregates stage results into a final Report.
func (e *Evaluator) Combine(stages ...CheckResult) Report {
	r := Report{Pass: true, Stages: stages}
	for _, s := range stages {
		if !s.Pass {
			r.Pass = false
		}
	}
	return r
}
