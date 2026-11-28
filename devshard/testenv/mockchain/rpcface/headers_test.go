package rpcface_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/observer"

	"devshard/testenv/mockchain/rpcface"
	"devshard/testenv/mockchain/seed"
)

func verifyingOracle(t *testing.T, url string) *observer.Oracle {
	t.Helper()
	oracle, err := observer.NewTendermint(observer.TendermintConfig{RPCURL: url, VerifyCommits: true})
	require.NoError(t, err)
	require.True(t, oracle.Verifying())
	return oracle
}

// Test flow:
//  1. Serve the mock chain over RPC with a fast block ticker and subscribe to its NewBlock events.
//  2. Build the commit-verifying oracle the gateway uses and read the height of a published block.
//  3. Assert the verified header carries the hash the NewBlock event announced and the chain id.
//  4. Read a height far below the trusted tip, which the oracle checks through the hash links.
//  5. Assert the older header is on the same chain and strictly older.
func TestMockChainRPC_VerifiedHeaderMatchesNewBlockEvent(t *testing.T) {
	svc, url, cleanup, err := rpcface.NewInProcessServer(seed.Defaults(), rpcface.Config{BlockInterval: 20 * time.Millisecond})
	require.NoError(t, err)
	t.Cleanup(cleanup)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sub, err := svc.EventBus().Subscribe(ctx, "headers-test", cmttypes.EventQueryNewBlock)
	require.NoError(t, err)

	var event cmttypes.EventDataNewBlock
	for range 3 {
		select {
		case msg := <-sub.Out():
			event = msg.Data().(cmttypes.EventDataNewBlock)
		case <-time.After(5 * time.Second):
			t.Fatal("no NewBlock event within 5s")
		}
	}

	oracle := verifyingOracle(t, url)
	got, err := oracle.At(ctx, event.Block.Height)
	require.NoError(t, err)
	require.Equal(t, event.BlockID.Hash.Bytes(), got.BlockHash)
	require.Equal(t, "gonka-test", got.ChainID)

	older, err := oracle.At(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, int64(100), older.Height)
	require.Equal(t, "gonka-test", older.ChainID)
	require.True(t, older.Time.Before(got.Time), "height 100 at %s, height %d at %s", older.Time, got.Height, got.Time)
}

// Test flow:
//  1. Advance an epoch, which publishes hundreds of blocks at once.
//  2. Read the new tip through the commit-verifying oracle.
//  3. Assert the tip verifies and its time is not ahead of the clock by more than the verifier's drift allowance.
func TestMockChainRPC_VerifiedTipAfterEpochAdvance(t *testing.T) {
	svc, url, cleanup, err := rpcface.NewInProcessServer(seed.Defaults(), rpcface.Config{BlockInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(cleanup)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := svc.AdvanceEpoch(ctx)
	require.NoError(t, err)

	got, err := verifyingOracle(t, url).At(ctx, resp.ToBlockHeight)
	require.NoError(t, err)
	require.Equal(t, resp.ToBlockHeight, got.Height)
	require.Less(t, time.Until(got.Time), observer.DefaultMaxClockDrift)
}

// Test flow:
//  1. Put a proxy in front of the mock chain that corrupts the first commit signature it relays.
//  2. Read a height through the commit-verifying oracle pointed at the proxy.
//  3. Assert the oracle refuses the header as unverified.
func TestMockChainRPC_VerifierRefusesACorruptedCommit(t *testing.T) {
	_, url, cleanup, err := rpcface.NewInProcessServer(seed.Defaults(), rpcface.Config{BlockInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(cleanup)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, _ := io.ReadAll(r.Body)
		upstream, err := http.Post(url, "application/json", bytes.NewReader(request))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer upstream.Body.Close()
		body, _ := io.ReadAll(upstream.Body)
		if bytes.Contains(request, []byte(`"method":"commit"`)) {
			body = corruptFirstSignature(body)
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(proxy.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, err = verifyingOracle(t, proxy.URL).At(ctx, 150)
	require.ErrorIs(t, err, blocks.ErrHeaderUnverified)
}

func corruptFirstSignature(response []byte) []byte {
	const marker = `"signature":"`
	at := bytes.Index(response, []byte(marker))
	if at < 0 {
		return response
	}
	at += len(marker)
	corrupted := append([]byte(nil), response...)
	corrupted[at] = 'A'
	if response[at] == 'A' {
		corrupted[at] = 'B'
	}
	return corrupted
}

// Test flow:
//  1. Serve the mock chain and read the validator set, the commit and the header at one height over RPC.
//  2. Assert the commit signs the served header and the set hashes to the header's validators hash.
//  3. Ask for a height above the tip and assert the refusal reads as a height error the verifier treats as not found.
func TestMockChainRPC_ServesCommitValidatorsAndHeader(t *testing.T) {
	_, url, cleanup, err := rpcface.NewInProcessServer(seed.Defaults(), rpcface.Config{BlockInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(cleanup)
	client, err := rpchttp.New(url, "/websocket")
	require.NoError(t, err)
	ctx := context.Background()
	height := int64(120)

	commit, err := client.Commit(ctx, &height)
	require.NoError(t, err)
	header, err := client.Header(ctx, &height)
	require.NoError(t, err)
	validators, err := client.Validators(ctx, &height, nil, nil)
	require.NoError(t, err)

	require.Equal(t, header.Header.Hash().Bytes(), commit.Commit.BlockID.Hash.Bytes())
	require.Equal(t, 1, validators.Total)
	require.Equal(t, header.Header.ValidatorsHash.Bytes(), cmttypes.NewValidatorSet(validators.Validators).Hash())

	_, err = client.Commit(ctx, ptr(int64(10_000)))
	require.ErrorContains(t, err, "must be less than or equal to the current blockchain height")
}

func ptr[T any](v T) *T { return &v }
