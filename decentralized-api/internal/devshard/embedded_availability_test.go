package devshard

import (
	"context"
	"testing"

	"decentralized-api/apiconfig"

	devshardpkg "devshard"
	"devshard/host"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// devshard/internal/testutil is module-internal; keep helpers local for dapi tests.
var embeddedTestPrompt = []byte(`{"model":"llama","messages":[{"role":"user","content":"prompt"}]}`)

func TestEmbeddedHost_ConfigManagerAvailabilityBlocksCompletion(t *testing.T) {
	cm := &apiconfig.ConfigManager{}
	cm.SetDevshardVersions(apiconfig.DevshardVersionsCache{DevshardRequestsEnabled: false})

	h := testHostWithAvailability(t, 1, NewConfigManagerAvailability(cm, nil))

	startDiff := signStartDiff(t, h.User, "escrow-1", 1)
	_, err := h.Host.HandleRequest(context.Background(), host.HostRequest{
		Diffs: []types.Diff{startDiff}, Nonce: 1, Payload: startInferencePayload(),
	})
	require.ErrorIs(t, err, devshardpkg.ErrRequestsDisabled)
}

type testHostFixture struct {
	Host *host.Host
	User *signing.Secp256k1Signer
}

func testHostWithAvailability(t *testing.T, hostIdx int, avail devshardpkg.AvailabilityProvider) testHostFixture {
	t.Helper()
	hosts := mustGenerateKeys(t, 3)
	user := mustGenerateKey(t)
	group := makeSlotGroup(hosts)
	config := defaultSessionConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	infStore := storage.NewMemory()
	require.NoError(t, infStore.CreateSession(storage.CreateSessionParams{
		EscrowID: "escrow-1", Version: runtimeTestVersion, CreatorAddr: user.Address(), Config: config, Group: group, InitialBalance: 10000,
	}))
	sm, err := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, infStore)
	require.NoError(t, err)
	h, err := host.NewHost(sm, hosts[hostIdx], stub.NewInferenceEngine(), "escrow-1", group, nil,
		host.WithGrace(10),
		host.WithAvailabilityProvider(avail),
	)
	require.NoError(t, err)
	return testHostFixture{Host: h, User: user}
}

func mustGenerateKeys(t *testing.T, n int) []*signing.Secp256k1Signer {
	t.Helper()
	out := make([]*signing.Secp256k1Signer, n)
	for i := range out {
		out[i] = mustGenerateKey(t)
	}
	return out
}

func makeSlotGroup(signers []*signing.Secp256k1Signer) []types.SlotAssignment {
	group := make([]types.SlotAssignment, len(signers))
	for i, s := range signers {
		group[i] = types.SlotAssignment{SlotID: uint32(i), ValidatorAddress: s.Address()}
	}
	return group
}

func defaultSessionConfig(numHosts int) types.SessionConfig {
	return types.NormalizeSessionConfig(types.SessionConfig{
		RefusalTimeout: 60, ExecutionTimeout: 1200, TokenPrice: 1,
		VoteThreshold: uint32(numHosts) / 2, ValidationRate: 5000,
	}, numHosts)
}

func signStartDiff(t *testing.T, signer signing.Signer, escrowID string, inferenceID uint64) types.Diff {
	t.Helper()
	promptHash, err := devshardpkg.CanonicalPromptHash(embeddedTestPrompt)
	require.NoError(t, err)
	txs := []*types.DevshardTx{{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
		InferenceId: inferenceID, PromptHash: promptHash, Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	}}}}
	content := &types.DiffContent{Nonce: 1, Txs: txs, EscrowId: escrowID}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(content)
	require.NoError(t, err)
	sig, err := signer.Sign(data)
	require.NoError(t, err)
	return types.Diff{Nonce: 1, Txs: txs, UserSig: sig}
}

func startInferencePayload() *host.InferencePayload {
	return &host.InferencePayload{
		Prompt: embeddedTestPrompt, Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	}
}
