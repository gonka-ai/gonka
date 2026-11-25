package heights

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/observer"

	"devshard/logging"
	"devshard/types"
)

const (
	headerTimeHeight = 42
	headerTimeUnixMs = 1_700_000_000_000
	otherUnixMs      = 1_700_000_999_000
	usableRPC        = "http://rpc.example:26657"
	malformedRPC     = "://not a url"
	grpcHost         = "chain.example:9090"
)

type headerAtFixedTime struct {
	blocks.BlockOracle
	hash   []byte
	unixMs int64
}

type verifiedAtFixedTime struct {
	blocks.BlockOracle
	hash   []byte
	unixMs int64
}

func (oracle verifiedAtFixedTime) AtWait(_ context.Context, height int64, _ time.Duration) (*blocks.Header, error) {
	return &blocks.Header{Height: height, BlockHash: oracle.hash, Time: time.UnixMilli(oracle.unixMs)}, nil
}

type unreachableOracle struct{ blocks.BlockOracle }

func (unreachableOracle) HeaderAtWait(context.Context, int64, time.Duration) (*blocks.Header, error) {
	return nil, errors.New("chain is unreachable")
}

func (oracle headerAtFixedTime) At(_ context.Context, height int64) (*blocks.Header, error) {
	return blocks.DummyHeader(height), nil
}

func (oracle headerAtFixedTime) HeaderAtWait(_ context.Context, height int64, _ time.Duration) (*blocks.Header, error) {
	return &blocks.Header{Height: height, BlockHash: oracle.hash, Time: time.UnixMilli(oracle.unixMs)}, nil
}

// Test flow:
//  1. Build the header time source over a follower whose oracle answers a plain lookup with a dummy header and a waiting lookup with the real block time.
//  2. Wait for the source to resolve that height with the oracle's block hash.
//  3. Assert it returns the block time of the waiting lookup, so the follower's history reads are not hidden behind its embedded interface.
func TestHeaderTimesReadTheFollowerWhenItIsOn(t *testing.T) {
	hash := []byte{0xab}
	follower := &Oracle{BlockOracle: headerAtFixedTime{hash: hash, unixMs: headerTimeUnixMs}}

	got, err := NewHeaderTimes(follower, nil, nil).AwaitHeaderTimeMs(t.Context(), headerTimeHeight, hash)

	if err != nil || got != headerTimeUnixMs {
		t.Fatalf("AwaitHeaderTimeMs() = %d, %v, want %d, nil", got, err, headerTimeUnixMs)
	}
}

// Test flow:
//  1. Build the header time source with no follower and no chain client.
//  2. Assert the source exists.
//  3. Ask it for a height and assert the answer is a pending header, not a panic.
func TestHeaderTimesExistWithoutAFollower(t *testing.T) {
	source := NewHeaderTimes(nil, nil, nil)

	if source == nil {
		t.Fatal("NewHeaderTimes(nil, nil, nil) = nil, want a source")
	}
	if _, err := source.HeaderTimeMs(headerTimeHeight, []byte{0xab}); !errors.Is(err, types.ErrHeaderPending) {
		t.Fatalf("HeaderTimeMs() error = %v, want %v", err, types.ErrHeaderPending)
	}
}

// Test flow:
//  1. Build the header time source over a verified oracle and a healthy follower that report different block times for the same height.
//  2. Wait for the source to resolve that height with the shared block hash.
//  3. Assert it returns the verified oracle's block time, so the follower's unverified read is not consulted when a verifier exists.
func TestHeaderTimesReadTheVerifiedSourceBeforeTheFollower(t *testing.T) {
	hash := []byte{0xab}
	verified := verifiedAtFixedTime{hash: hash, unixMs: headerTimeUnixMs}
	follower := &Oracle{BlockOracle: headerAtFixedTime{hash: hash, unixMs: otherUnixMs}}

	got, err := NewHeaderTimes(follower, nil, verified).AwaitHeaderTimeMs(t.Context(), headerTimeHeight, hash)

	if err != nil || got != headerTimeUnixMs {
		t.Fatalf("AwaitHeaderTimeMs() = %d, %v, want %d, nil", got, err, headerTimeUnixMs)
	}
}

// Test flow:
//  1. Build the header time source over a healthy verified oracle and a follower whose chain reads always fail.
//  2. Wait for the source to resolve a height.
//  3. Assert the verified block time is returned, so header reads survive an unreachable chain gRPC.
func TestHeaderTimesSurviveAnUnreachableFollowerWhenTheVerifiedSourceIsHealthy(t *testing.T) {
	hash := []byte{0xab}
	verified := verifiedAtFixedTime{hash: hash, unixMs: headerTimeUnixMs}
	follower := &Oracle{BlockOracle: unreachableOracle{}}

	got, err := NewHeaderTimes(follower, nil, verified).AwaitHeaderTimeMs(t.Context(), headerTimeHeight, hash)

	if err != nil || got != headerTimeUnixMs {
		t.Fatalf("AwaitHeaderTimeMs() = %d, %v, want %d, nil", got, err, headerTimeUnixMs)
	}
}

// Test flow:
//  1. Build the commit-verifying client for a usable endpoint, and the commit-verified oracle for no endpoint at all and for text that is not a URL.
//  2. Assert the usable endpoint yields a client that verifies commit signatures.
//  3. Assert no endpoint and a malformed one yield no oracle, so the header source falls back instead of holding a broken verifier.
func TestOnlyAUsableRPCEndpointBuildsACommitVerifier(t *testing.T) {
	verifier, err := newCommitVerifier(usableRPC)

	if oracle, isOracle := verifier.(*observer.Oracle); err != nil || !isOracle || !oracle.Verifying() {
		t.Fatalf("newCommitVerifier(%q) = %v, %v, want a client that verifies commits", usableRPC, verifier, err)
	}
	for _, endpoint := range []string{"", "  ", malformedRPC} {
		if oracle := CommitVerifiedOracle("", endpoint); oracle != nil {
			t.Fatalf("CommitVerifiedOracle(%q, %q) = %v, want nil", "", endpoint, oracle)
		}
	}
}

// Test flow:
//  1. Build the header time source with a healthy follower and the verified oracle of an endpoint that is not a URL.
//  2. Wait for the source to resolve a height.
//  3. Assert the follower's block time is returned, so a verifier that cannot be built falls back instead of leaving the gateway without header times.
func TestHeaderTimesFallBackWhenTheRPCEndpointCannotBuildAVerifier(t *testing.T) {
	hash := []byte{0xab}
	follower := &Oracle{BlockOracle: headerAtFixedTime{hash: hash, unixMs: headerTimeUnixMs}}

	got, err := NewHeaderTimes(follower, nil, CommitVerifiedOracle(grpcHost, malformedRPC)).AwaitHeaderTimeMs(t.Context(), headerTimeHeight, hash)

	if err != nil || got != headerTimeUnixMs {
		t.Fatalf("AwaitHeaderTimeMs() = %d, %v, want %d, nil", got, err, headerTimeUnixMs)
	}
}

type latestOracle struct {
	blocks.BlockOracle
	err error
}

func (oracle latestOracle) Latest(context.Context) (*blocks.Header, error) {
	if oracle.err != nil {
		return nil, oracle.err
	}
	return blocks.DummyHeader(headerTimeHeight), nil
}

type capturedLogs struct {
	logging.Logger
	mu       sync.Mutex
	warnings []string
}

func (logs *capturedLogs) Warn(msg string, keyvals ...any) {
	logs.mu.Lock()
	defer logs.mu.Unlock()
	logs.warnings = append(logs.warnings, fmt.Sprint(msg, " ", keyvals))
}

func captureWarnings(t *testing.T) *capturedLogs {
	t.Helper()
	logs := &capturedLogs{Logger: logging.NewSlogAdapter()}
	logging.SetLogger(logs)
	t.Cleanup(func() { logging.SetLogger(logging.NewSlogAdapter()) })
	return logs
}

func (logs *capturedLogs) all() string {
	logs.mu.Lock()
	defer logs.mu.Unlock()
	return strings.Join(logs.warnings, "\n")
}

// Test flow:
//  1. Probe a derived endpoint whose verifying client answers.
//  2. Assert the verifying client is kept.
//  3. Assert no warning was logged.
func TestADerivedEndpointThatAnswersKeepsTheVerifiedSource(t *testing.T) {
	logs := captureWarnings(t)
	answering := latestOracle{}

	got := probedVerifiedOracle(answering, true)

	if got != blocks.BlockOracle(answering) {
		t.Fatalf("probedVerifiedOracle(answering, derived) = %v, want the verifying client", got)
	}
	if warnings := logs.all(); warnings != "" {
		t.Fatalf("warnings = %q, want none", warnings)
	}
}

// Test flow:
//  1. Probe a derived endpoint whose verifying client fails, then build the header time source with it over a healthy follower.
//  2. Assert the probe yields no oracle and logs one warning that says the derived endpoint does not answer.
//  3. Assert the source reads the follower's block time, so a deployment that does not serve RPC on the derived port keeps the unverified read instead of stalling.
func TestADerivedEndpointThatDoesNotAnswerFallsBackToTheUnverifiedRead(t *testing.T) {
	logs := captureWarnings(t)
	hash := []byte{0xab}
	follower := &Oracle{BlockOracle: headerAtFixedTime{hash: hash, unixMs: headerTimeUnixMs}}

	verified := probedVerifiedOracle(latestOracle{err: errors.New("connection refused")}, true)
	got, err := NewHeaderTimes(follower, nil, verified).AwaitHeaderTimeMs(t.Context(), headerTimeHeight, hash)

	if verified != nil {
		t.Fatalf("probedVerifiedOracle(failing, derived) = %v, want nil", verified)
	}
	if err != nil || got != headerTimeUnixMs {
		t.Fatalf("AwaitHeaderTimeMs() = %d, %v, want %d, nil", got, err, headerTimeUnixMs)
	}
	if warnings := logs.all(); !strings.Contains(warnings, "derived from the gRPC host does not answer") {
		t.Fatalf("warnings = %q, want one naming the derived endpoint", warnings)
	}
}

// Test flow:
//  1. Probe an explicitly set endpoint whose verifying client fails.
//  2. Assert the verifying client is kept, so there is no silent downgrade to the unverified read.
//  3. Assert one warning says the configured endpoint does not answer.
func TestAnExplicitEndpointThatDoesNotAnswerStaysVerified(t *testing.T) {
	logs := captureWarnings(t)
	failing := latestOracle{err: errors.New("connection refused")}

	got := probedVerifiedOracle(failing, false)

	if got != blocks.BlockOracle(failing) {
		t.Fatalf("probedVerifiedOracle(failing, explicit) = %v, want the verifying client", got)
	}
	if warnings := logs.all(); !strings.Contains(warnings, "DEVSHARD_CHAIN_RPC does not answer") {
		t.Fatalf("warnings = %q, want one naming the configured endpoint", warnings)
	}
}

func buildFailingOracle(string) (blocks.BlockOracle, error) {
	return latestOracle{err: errors.New("dial " + usableRPC)}, nil
}

// Test flow:
//  1. Resolve the commit-verified oracle over a failing client for a gRPC host with a blank RPC endpoint, and with an explicit one.
//  2. Assert the blank endpoint counts as derived, so the oracle is dropped, and the explicit one is kept.
//  3. Assert the warnings of both calls carry neither endpoint URL.
func TestOnlyABlankRPCEndpointCountsAsDerived(t *testing.T) {
	logs := captureWarnings(t)

	derived := commitVerifiedOracle(grpcHost, "  ", buildFailingOracle)
	explicit := commitVerifiedOracle(grpcHost, usableRPC, buildFailingOracle)

	if derived != nil {
		t.Fatalf("commitVerifiedOracle(grpc, blank) = %v, want nil", derived)
	}
	if explicit == nil {
		t.Fatal("commitVerifiedOracle(grpc, explicit) = nil, want the verifying client")
	}
	if warnings := logs.all(); strings.Contains(warnings, usableRPC) || strings.Contains(warnings, grpcHost) {
		t.Fatalf("warnings = %q, want no endpoint URL", warnings)
	}
}

// Test flow:
//  1. Capture warnings and resolve the commit-verified oracle for an endpoint that is not a URL.
//  2. Assert there is no oracle.
//  3. Assert a warning was logged and none of the warnings contains the endpoint text, since the client's own error quotes it.
func TestAMalformedEndpointIsNeverLogged(t *testing.T) {
	logs := captureWarnings(t)

	got := CommitVerifiedOracle(grpcHost, malformedRPC)

	if got != nil {
		t.Fatalf("CommitVerifiedOracle(grpc, malformed) = %v, want nil", got)
	}
	if warnings := logs.all(); warnings == "" || strings.Contains(warnings, malformedRPC) {
		t.Fatalf("warnings = %q, want one without the endpoint text", warnings)
	}
}
