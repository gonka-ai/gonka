package state

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/lightproof"
	"devshard/types"
)

// MainnetHeightSource supplies untrusted evidence. Only lightproof.Verify may
// advance consensus state; neither Latest nor a self-signed validator set is a
// trust anchor.
type MainnetHeightSource interface {
	lightproof.Source
	Latest(context.Context) (*blocks.Header, error)
}

// Prelude construction can run while the sequencer holds its session lock.
// Bound each checkpoint RPC sequence even for callers without a deadline.
const mainnetCheckpointRPCTimeout = 10 * time.Second

func WithMainnetHeights(source MainnetHeightSource) SMOption {
	return func(sm *StateMachine) { sm.mainnetHeights = source }
}

func cloneCheckpoint(cp lightproof.Checkpoint) lightproof.Checkpoint {
	cp.Hash = bytes.Clone(cp.Hash)
	cp.NextValidatorsHash = bytes.Clone(cp.NextValidatorsHash)
	return cp
}

func (sm *StateMachine) MainnetCheckpoint() lightproof.Checkpoint {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return cloneCheckpoint(sm.state.MainnetCheckpoint)
}

// BuildMainnetHeightTx is the sequencer prelude, including finalization diffs.
// A repeat of an already verified height carries no blocks and makes no proof
// RPCs. Header advances carry all necessary validator transitions for replay.
func (sm *StateMachine) BuildMainnetHeightTx(ctx context.Context) (*types.DevshardTx, error) {
	sm.mu.RLock()
	required := types.RequiresMainnetHeightProof(sm.state.StateRootAndProtocolVersion)
	cp := cloneCheckpoint(sm.state.MainnetCheckpoint)
	source := sm.mainnetHeights
	sm.mu.RUnlock()
	if !required {
		return nil, nil
	}
	// The inference chooser may omit its optional persistence context.
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, mainnetCheckpointRPCTimeout)
	defer cancel()
	header, err := latestMainnetHeader(ctx, source)
	if err != nil {
		return nil, err
	}
	next, proof, err := authenticateMainnetTip(ctx, cp, header, source)
	if err != nil {
		return nil, err
	}
	return &types.DevshardTx{Tx: &types.DevshardTx_MainnetHeight{MainnetHeight: &types.MsgMainnetHeight{Height: next.Height, LightBlocks: proof, TimeUnixNano: next.Time.UnixNano()}}}, nil
}

func latestMainnetHeader(ctx context.Context, source MainnetHeightSource) (*blocks.Header, error) {
	if source == nil {
		return nil, fmt.Errorf("%w: no mainnet height source", types.ErrHeaderPending)
	}
	header, err := source.Latest(ctx)
	if err != nil || header == nil || blocks.IsDummyHeader(header) {
		return nil, fmt.Errorf("%w: mainnet tip unavailable: %v", types.ErrHeaderPending, err)
	}
	return header, nil
}

// authenticateMainnetTip returns fresh evidence without applying it or creating
// a transaction. The sequencer serializes the proof into its next diff; final
// attestation uses only the checkpoint to authorize the existing final state.
// Callers share a bounded context across the tip lookup and proof reads.
func authenticateMainnetTip(ctx context.Context, from lightproof.Checkpoint, tip *blocks.Header, source lightproof.Source) (lightproof.Checkpoint, [][]byte, error) {
	target := max(tip.Height, from.Height)
	proof, next, err := lightproof.Build(ctx, from, target, source)
	if err != nil {
		return lightproof.Checkpoint{}, nil, fmt.Errorf("mainnet height proof: %w", err)
	}
	if target == tip.Height && !bytes.Equal(next.Hash, tip.BlockHash) {
		return lightproof.Checkpoint{}, nil, fmt.Errorf("%w: light block differs from advertised mainnet tip", lightproof.ErrInvalid)
	}
	if err := checkCheckpointAge(next, time.Now()); err != nil {
		return lightproof.Checkpoint{}, nil, err
	}
	return next, proof, nil
}

// stageMainnetHeightLocked runs inside the outer mutation journal, before any
// message effect or authorization check. It is mandatory on *all* v7 apply
// paths, including persisted replay. No clock or RPC is consulted here.
func (sm *StateMachine) stageMainnetHeightLocked(txs []*types.DevshardTx) error {
	required := types.RequiresMainnetHeightProof(sm.state.StateRootAndProtocolVersion)
	count := 0
	var msg *types.MsgMainnetHeight
	for _, tx := range txs {
		if h := tx.GetMainnetHeight(); h != nil {
			count++
			msg = h
		}
	}
	if !required {
		if count != 0 {
			return fmt.Errorf("%w: mainnet proof requires v7", types.ErrProtocolVersionMismatch)
		}
		return nil
	}
	if count != 1 || len(txs) == 0 || txs[0].GetMainnetHeight() == nil {
		return fmt.Errorf("%w: v7 diff must begin with exactly one mainnet height proof", lightproof.ErrInvalid)
	}
	next, err := lightproof.Verify(sm.state.MainnetCheckpoint, msg.Height, msg.LightBlocks)
	if err != nil {
		return err
	}
	if msg.TimeUnixNano != next.Time.UnixNano() {
		return fmt.Errorf("%w: claimed checkpoint time", lightproof.ErrInvalid)
	}
	sm.state.MainnetCheckpoint = next // replaced, never edited in place; journal owns rollback
	return nil
}

// Freshness is ordinary live signing policy, not replay validity. New work
// cannot keep collecting signatures against a frozen old height. Recovery of
// an admitted final root uses separate fresh authority evidence.
const MainnetCheckpointMaxAge = 2 * time.Minute
const MainnetCheckpointMaxFuture = 10 * time.Second
const MainnetCheckpointMaxLag = int64(2)

func checkCheckpointAge(cp lightproof.Checkpoint, now time.Time) error {
	if cp.Time.IsZero() || now.Sub(cp.Time) > MainnetCheckpointMaxAge || cp.Time.Sub(now) > MainnetCheckpointMaxFuture {
		return fmt.Errorf("%w: mainnet checkpoint is outside the live signing time window", types.ErrHeaderPending)
	}
	return nil
}

func checkCheckpointLag(cp lightproof.Checkpoint, tip *blocks.Header) error {
	if tip.Height > cp.Height+MainnetCheckpointMaxLag {
		return fmt.Errorf("%w: diff height %d trails local tip %d", types.ErrHeaderPending, cp.Height, tip.Height)
	}
	return nil
}

func (sm *StateMachine) CheckMainnetHeightFreshness(ctx context.Context, now time.Time) error {
	sm.mu.RLock()
	required := types.RequiresMainnetHeightProof(sm.state.StateRootAndProtocolVersion)
	cp := cloneCheckpoint(sm.state.MainnetCheckpoint)
	source := sm.mainnetHeights
	headerTimes := sm.headerTimes
	sm.mu.RUnlock()
	if !required {
		return nil
	}
	if err := checkCheckpointAge(cp, now); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, mainnetCheckpointRPCTimeout)
	defer cancel()
	latest, err := latestMainnetHeader(ctx, source)
	if err != nil {
		return err
	}
	if err := checkCheckpointLag(cp, latest); err != nil {
		return err
	}
	// Live signers also reconcile the carried proof with their own canonical
	// oracle. This prevents an old escrow anchor being used for a long-range
	// fork after its old validator keys have left the trusting period. Replay
	// remains independent of that mutable source.
	if err := checkCanonicalCheckpoint(ctx, cp, source, headerTimes); err != nil {
		return err
	}
	return nil
}

func checkCanonicalCheckpoint(ctx context.Context, cp lightproof.Checkpoint, source MainnetHeightSource, headerTimes HeaderTimeSource) error {
	if oracle, ok := source.(interface {
		CanonicalHeaderForProof(context.Context, int64) (*blocks.Header, error)
	}); ok {
		local, err := oracle.CanonicalHeaderForProof(ctx, cp.Height)
		if !errors.Is(err, blocks.ErrProveNotImplemented) {
			return matchCanonicalCheckpoint(cp, local, err)
		}
	}
	if headerTimes != nil {
		_, err := headerTimes.HeaderTimeMs(uint64(cp.Height), cp.Hash)
		return err
	}
	if oracle, ok := source.(blocks.BlockOracle); ok {
		local, err := oracle.At(ctx, cp.Height)
		return matchCanonicalCheckpoint(cp, local, err)
	}
	return fmt.Errorf("%w: no canonical checkpoint lookup", types.ErrHeaderPending)
}

func matchCanonicalCheckpoint(cp lightproof.Checkpoint, local *blocks.Header, err error) error {
	if err != nil || local == nil || blocks.IsDummyHeader(local) || local.Height != cp.Height {
		return fmt.Errorf("%w: canonical checkpoint unavailable: %v", types.ErrHeaderPending, err)
	}
	if !bytes.Equal(local.BlockHash, cp.Hash) {
		return fmt.Errorf("%w: canonical checkpoint hash", types.ErrHeaderMismatch)
	}
	return nil
}

// validateSnapshotCheckpoint authenticates snapshot trust metadata independently
// of the snapshot's own state signatures. Otherwise an expired warm key could
// vouch for a fabricated header time or validator set and bootstrap its own
// authority. The anchor is the original chain-derived session config, never a
// replacement anchor claimed by the snapshot.
func (sm *StateMachine) validateSnapshotCheckpoint(snapshot *types.EscrowState) error {
	sm.mu.RLock()
	expectedVersion := sm.state.StateRootAndProtocolVersion
	anchor := lightproof.Checkpoint{Height: sm.state.Config.MainnetAnchorHeight, Hash: bytes.Clone(sm.state.Config.MainnetAnchorHash)}
	source := sm.mainnetHeights
	escrowID := sm.state.EscrowID
	sm.mu.RUnlock()
	required := types.RequiresMainnetHeightProof(expectedVersion)
	if !required && !types.RequiresMainnetHeightProof(snapshot.StateRootAndProtocolVersion) {
		return nil
	}
	if !required || snapshot.StateRootAndProtocolVersion != expectedVersion || snapshot.EscrowID != escrowID {
		return fmt.Errorf("%w: snapshot session identity/version", types.ErrProtocolVersionMismatch)
	}
	if snapshot.Config.MainnetAnchorHeight != anchor.Height || !bytes.Equal(snapshot.Config.MainnetAnchorHash, anchor.Hash) {
		return fmt.Errorf("%w: snapshot replaced the chain-pinned anchor", lightproof.ErrInvalid)
	}
	got := snapshot.MainnetCheckpoint
	if snapshot.LatestNonce == 0 && got.Height == anchor.Height && bytes.Equal(got.Hash, anchor.Hash) && len(got.NextValidatorsHash) == 0 && got.Time.IsZero() && got.ChainID == "" {
		return nil
	}
	if source == nil {
		return fmt.Errorf("%w: snapshot checkpoint evidence unavailable", types.ErrHeaderPending)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, expected, err := lightproof.Build(ctx, anchor, got.Height, source)
	if err != nil {
		return fmt.Errorf("snapshot mainnet checkpoint: %w", err)
	}
	if expected.Height != got.Height || !bytes.Equal(expected.Hash, got.Hash) || !bytes.Equal(expected.NextValidatorsHash, got.NextValidatorsHash) || !expected.Time.Equal(got.Time) || expected.ChainID != got.ChainID {
		return fmt.Errorf("%w: snapshot checkpoint differs from authenticated headers", lightproof.ErrInvalid)
	}
	return nil
}
