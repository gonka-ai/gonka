// Phase 7 acceptance tests (session-config-flow-plan.md):
// VoteThreshold is owned by the state machine; bind-time freeze and legacy fee defaults.

package state

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func TestNewStateMachine_BindVoteThreshold_GovernanceFactor67(t *testing.T) {
	const groupSize = 6
	hosts := make([]*signing.Secp256k1Signer, groupSize)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)

	cfg := types.SessionConfigFromEscrow(groupSize, types.EscrowSessionFields{
		VoteThresholdFactor: 67,
	})
	require.Equal(t, uint32(4), cfg.VoteThreshold)

	verifier := signing.NewSecp256k1Verifier()
	sm, err := NewStateMachine("escrow-1", cfg, group, 10_000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), cfg, group, 10_000))
	require.NoError(t, err)
	require.Equal(t, uint32(4), sm.VoteThreshold())
}

func TestVoteThreshold_StableAcrossValidationAndTimeout(t *testing.T) {
	const (
		groupSize      = 6
		voteThreshold  = uint32(4) // floor(6 * 67 / 100)
		acceptsNeeded  = 5         // need > voteThreshold for timeout
	)
	hosts := make([]*signing.Secp256k1Signer, groupSize)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	cfg := types.SessionConfigFromEscrow(groupSize, types.EscrowSessionFields{
		VoteThresholdFactor: 67,
	})
	verifier := signing.NewSecp256k1Verifier()
	sm, err := NewStateMachine("escrow-1", cfg, group, 100_000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), cfg, group, 100_000))
	require.NoError(t, err)

	require.Equal(t, voteThreshold, sm.VoteThreshold())

	// Pending inference; executor slot = group[1 % 6].SlotID.
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("p"), Model: "m",
		InputLength: 10, MaxTokens: 5, StartedAt: 1,
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)
	require.Equal(t, voteThreshold, sm.VoteThreshold())

	// Four accept timeout votes: weight 4 is not > threshold 4.
	votes := make([]*types.TimeoutVote, 4)
	for i := uint32(0); i < 4; i++ {
		v := testutil.SignTimeoutVote(t, hosts[i], "escrow-1", 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, true)
		v.VoterSlot = i
		votes[i] = v
	}
	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txTimeout(&types.MsgTimeoutInference{
		InferenceId: 1, Reason: types.TimeoutReason_TIMEOUT_REASON_REFUSED, Votes: votes,
	})})
	_, err = sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrInsufficientVotes)
	require.Equal(t, voteThreshold, sm.VoteThreshold())

	// Fifth accept vote resolves timeout; threshold unchanged.
	v5 := testutil.SignTimeoutVote(t, hosts[4], "escrow-1", 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, true)
	v5.VoterSlot = 4
	votes = append(votes, v5)
	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txTimeout(&types.MsgTimeoutInference{
		InferenceId: 1, Reason: types.TimeoutReason_TIMEOUT_REASON_REFUSED, Votes: votes,
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)
	require.Equal(t, types.StatusTimedOut, sm.SnapshotState().Inferences[1].Status)
	require.Equal(t, voteThreshold, sm.VoteThreshold())
}

func TestNewStateMachine_LegacyZeroFeePerNonceUsesCompiledDefault(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)

	cfg := types.SessionConfigFromEscrow(len(group), types.EscrowSessionFields{})
	require.Equal(t, types.DefaultSessionConfig(len(group)).FeePerNonce, cfg.FeePerNonce)

	verifier := signing.NewSecp256k1Verifier()
	sm, err := NewStateMachine("escrow-1", cfg, group, 10_000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), cfg, group, 10_000))
	require.NoError(t, err)
	require.Equal(t, uint64(1_000), sm.SnapshotState().Config.FeePerNonce)
}

// TestVoteThreshold_ConfigFieldNotUsedOutsideStatePackage ensures consensus code
// does not read SessionConfig.VoteThreshold directly outside devshard/state.
// Observability (proxy /status) and bind (types/config.go) are allowlisted.
func TestVoteThreshold_ConfigFieldNotUsedOutsideStatePackage(t *testing.T) {
	root := repoRoot(t)
	allowSubstrings := []string{
		string(filepath.Join("devshard", "state")),
		string(filepath.Join("devshard", "types", "config.go")),
		string(filepath.Join("devshard", "bridge", "config.go")),
		string(filepath.Join("devshard", "cmd", "devshardctl", "proxy.go")),
	}
	var violations []string
	scanRoots := []string{
		filepath.Join(root, "devshard"),
	}
	for _, scanRoot := range scanRoots {
		if _, err := os.Stat(scanRoot); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			require.NoError(t, err)
		}
		err := filepath.Walk(scanRoot, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			for _, allow := range allowSubstrings {
				if strings.Contains(path, allow) {
					return nil
				}
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(data), "Config.VoteThreshold") ||
				strings.Contains(string(data), "config.VoteThreshold") {
				violations = append(violations, path)
			}
			return nil
		})
		require.NoError(t, err)
	}
	require.Empty(t, violations, "VoteThreshold must be read via state.StateMachine outside allowlisted bind/status paths")
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	// devshard/state/vote_threshold_freeze_test.go -> repo root (gonka module).
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
