package user

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"common/chainoracle/blocks"
	mockoracle "devshard/chainoracle/blocks/observer"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/types"
	"github.com/stretchr/testify/require"
)

func TestRecoverMainnetSnapshotWithoutOracleReplaysCarriedProofs(t *testing.T) {
	validator := testutil.MustGenerateKey(t)
	addr, err := blocks.AddressBytes(validator.CompressedPublicKeyBytes())
	require.NoError(t, err)
	source, err := mockoracle.NewMock(mockoracle.MockConfig{LightBlocks: true, ChainID: "recover-height", Validators: []mockoracle.MockValidator{{Signer: validator, Address: addr, Power: 10}}, InitialHeight: 100, Start: time.Now().Add(-10 * time.Second), BlockInterval: time.Second})
	require.NoError(t, err)
	anchor, err := source.AdvanceOne()
	require.NoError(t, err)
	_, err = source.AdvanceOne()
	require.NoError(t, err)
	cfg := testutil.DefaultConfig(1)
	cfg.MainnetAnchorHeight, cfg.MainnetAnchorHash = anchor.Height, bytes.Clone(anchor.BlockHash)
	user, host := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{host})
	store := newTestStore(t)
	verifier := signing.NewSecp256k1Verifier()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{EscrowID: "recover-height", Version: "v7", CreatorAddr: user.Address(), Config: cfg, Group: group, InitialBalance: 1_000_000}))
	sm, err := state.NewStateMachine("recover-height", cfg, group, 1_000_000, user.Address(), verifier, store, state.WithVersion("v7"), state.WithMainnetHeights(source))
	require.NoError(t, err)
	session, err := NewSession(sm, user, "recover-height", group, make([]HostClient, len(group)), verifier, WithStorage(store))
	require.NoError(t, err)
	session.mu.Lock()
	_, _, err = session.composeDiffLocked(nil, nil)
	session.mu.Unlock()
	require.NoError(t, err)
	root, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	require.NoError(t, writeSnapshotErr(store, "recover-height", 1, sm.ExportState(), map[int]uint64{0: 1}, sm.ExportCommittedEntries(), sm.ExportSealedNonces(), sm.ExportHeightSyncFloor()))
	// A snapshot needs external bootstrap evidence; the full journal carries it.
	recovered, restored, err := RecoverSession(store, user, verifier, "recover-height", "v7", group, make([]HostClient, len(group)))
	require.NoError(t, err)
	require.Equal(t, uint64(1), recovered.Nonce())
	replayedRoot, err := restored.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, root, replayedRoot)
	_, _, err = RecoverSession(missingMainnetPrefix{store}, user, verifier, "recover-height", "v7", group, make([]HostClient, len(group)))
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrLocalStateUnrecoverable), "unverifiable snapshot plus missing journal must fail closed: %v", err)

}

// Dropping an unverifiable snapshot is safe only if its full prefix is present.
type missingMainnetPrefix struct{ storage.Storage }

func (s missingMainnetPrefix) GetDiffs(string, uint64, uint64) ([]types.DiffRecord, error) {
	return nil, nil
}
