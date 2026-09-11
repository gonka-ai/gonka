package pocchallenge

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const MinPunishableSegmentBlocks int64 = 300

type Store struct {
	Challenges  collections.Map[sdk.AccAddress, types.PoCChallenge]
	Commits     collections.Map[collections.Quad[sdk.AccAddress, int64, string, uint32], types.PoCChallengeCommit]
	Validations collections.Map[collections.Quad[sdk.AccAddress, int64, string, sdk.AccAddress], types.PoCChallengeValidation]
	SkipSet     collections.KeySet[collections.Pair[int64, sdk.AccAddress]]
}

func NewStore(sb *collections.SchemaBuilder, cdc codec.BinaryCodec) *Store {
	return &Store{
		Challenges: collections.NewMap(
			sb,
			types.PoCChallengePrefix,
			"poc_challenge",
			sdk.AccAddressKey,
			codec.CollValue[types.PoCChallenge](cdc),
		),
		Commits: collections.NewMap(
			sb,
			types.PoCChallengeCommitPrefix,
			"poc_challenge_commit",
			collections.QuadKeyCodec(sdk.AccAddressKey, collections.Int64Key, collections.StringKey, collections.Uint32Key),
			codec.CollValue[types.PoCChallengeCommit](cdc),
		),
		Validations: collections.NewMap(
			sb,
			types.PoCChallengeValidationPrefix,
			"poc_challenge_validation",
			collections.QuadKeyCodec(sdk.AccAddressKey, collections.Int64Key, collections.StringKey, sdk.AccAddressKey),
			codec.CollValue[types.PoCChallengeValidation](cdc),
		),
		SkipSet: collections.NewKeySet(
			sb,
			types.ConfirmationEvaluationSkipPrefix,
			"poc_challenge_skip_cpoc",
			collections.PairKeyCodec(collections.Int64Key, sdk.AccAddressKey),
		),
	}
}

func (s *Store) Get(ctx context.Context, target string) (types.PoCChallenge, bool, error) {
	addr, err := sdk.AccAddressFromBech32(target)
	if err != nil {
		return types.PoCChallenge{}, false, err
	}
	ch, err := s.Challenges.Get(ctx, addr)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return types.PoCChallenge{}, false, nil
		}
		return types.PoCChallenge{}, false, err
	}
	return ch, true, nil
}

func (s *Store) Set(ctx context.Context, ch types.PoCChallenge) error {
	addr, err := sdk.AccAddressFromBech32(ch.Target)
	if err != nil {
		return err
	}
	return s.Challenges.Set(ctx, addr, ch)
}

func (s *Store) HasOpenChallenge(ctx context.Context, target string) bool {
	_, found, err := s.Get(ctx, target)
	return err == nil && found
}

func IsGenerating(ch types.PoCChallenge) bool {
	return ch.GenerationEndHeight == 0
}

func (s *Store) IsChallengeGenerating(ctx context.Context, target string) bool {
	ch, found, err := s.Get(ctx, target)
	if err != nil || !found {
		return false
	}
	return IsGenerating(ch)
}

func (s *Store) ListOpen(ctx context.Context) ([]types.PoCChallenge, error) {
	iter, err := s.Challenges.Iterate(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	return iter.Values()
}

func (s *Store) CountOpen(ctx context.Context) (int, error) {
	list, err := s.ListOpen(ctx)
	if err != nil {
		return 0, err
	}
	return len(list), nil
}

func (s *Store) OpenSegment(ch types.PoCChallenge) *types.PoCChallengeSegment {
	for i := range ch.Segments {
		if ch.Segments[i].SealHeight == 0 {
			return ch.Segments[i]
		}
	}
	return nil
}

func (s *Store) SegmentByStart(ch types.PoCChallenge, start int64) *types.PoCChallengeSegment {
	for i := range ch.Segments {
		if ch.Segments[i].PocStageStartBlockHeight == start {
			return ch.Segments[i]
		}
	}
	return nil
}

func (s *Store) ReplaceSegment(ch *types.PoCChallenge, updated types.PoCChallengeSegment) {
	for i := range ch.Segments {
		if ch.Segments[i].PocStageStartBlockHeight == updated.PocStageStartBlockHeight {
			ch.Segments[i] = &updated
			return
		}
	}
	ch.Segments = append(ch.Segments, &updated)
}

func (s *Store) ConfirmationEvaluationSkipSet(ctx context.Context, triggerHeight int64) (map[string]struct{}, error) {
	out := make(map[string]struct{})
	rng := collections.NewPrefixedPairRange[int64, sdk.AccAddress](triggerHeight)
	iter, err := s.SkipSet.Iterate(ctx, rng)
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			return nil, err
		}
		out[key.K2().String()] = struct{}{}
	}
	return out, nil
}

func (s *Store) DeleteSkipSet(ctx context.Context, triggerHeight int64) error {
	rng := collections.NewPrefixedPairRange[int64, sdk.AccAddress](triggerHeight)
	iter, err := s.SkipSet.Iterate(ctx, rng)
	if err != nil {
		return err
	}
	var keys []collections.Pair[int64, sdk.AccAddress]
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			iter.Close()
			return err
		}
		keys = append(keys, key)
	}
	iter.Close()
	for _, key := range keys {
		if err := s.SkipSet.Remove(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SnapshotSkip(ctx context.Context, triggerHeight int64) (map[string]struct{}, error) {
	out := make(map[string]struct{})
	list, err := s.ListOpen(ctx)
	if err != nil {
		return nil, err
	}
	for _, ch := range list {
		if !IsGenerating(ch) {
			continue
		}
		addr, err := sdk.AccAddressFromBech32(ch.Target)
		if err != nil {
			return nil, err
		}
		if err := s.SkipSet.Set(ctx, collections.Join(triggerHeight, addr)); err != nil {
			return nil, err
		}
		out[ch.Target] = struct{}{}
	}
	return out, nil
}

func commitKey(target sdk.AccAddress, start int64, modelID string, slice uint32) collections.Quad[sdk.AccAddress, int64, string, uint32] {
	return collections.Join4(target, start, modelID, slice)
}

func validationKey(target sdk.AccAddress, start int64, modelID string, validator sdk.AccAddress) collections.Quad[sdk.AccAddress, int64, string, sdk.AccAddress] {
	return collections.Join4(target, start, modelID, validator)
}

func (s *Store) GetCommit(ctx context.Context, target string, start int64, modelID string, slice uint32) (types.PoCChallengeCommit, bool, error) {
	addr, err := sdk.AccAddressFromBech32(target)
	if err != nil {
		return types.PoCChallengeCommit{}, false, err
	}
	c, err := s.Commits.Get(ctx, commitKey(addr, start, modelID, slice))
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return types.PoCChallengeCommit{}, false, nil
		}
		return types.PoCChallengeCommit{}, false, err
	}
	return c, true, nil
}

func (s *Store) SetCommit(ctx context.Context, c types.PoCChallengeCommit) error {
	addr, err := sdk.AccAddressFromBech32(c.Target)
	if err != nil {
		return err
	}
	return s.Commits.Set(ctx, commitKey(addr, c.PocStageStartBlockHeight, c.ModelId, c.SliceIndex), c)
}

func (s *Store) ListCommitsForSegment(ctx context.Context, target string, start int64) ([]types.PoCChallengeCommit, error) {
	addr, err := sdk.AccAddressFromBech32(target)
	if err != nil {
		return nil, err
	}
	rng := collections.NewSuperPrefixedQuadRange[sdk.AccAddress, int64, string, uint32](addr, start)
	iter, err := s.Commits.Iterate(ctx, rng)
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	return iter.Values()
}

func (s *Store) HasValidation(ctx context.Context, target string, start int64, modelID, validator string) (bool, error) {
	tAddr, err := sdk.AccAddressFromBech32(target)
	if err != nil {
		return false, err
	}
	vAddr, err := sdk.AccAddressFromBech32(validator)
	if err != nil {
		return false, err
	}
	return s.Validations.Has(ctx, validationKey(tAddr, start, modelID, vAddr))
}

func (s *Store) SetValidation(ctx context.Context, v types.PoCChallengeValidation) error {
	tAddr, err := sdk.AccAddressFromBech32(v.Target)
	if err != nil {
		return err
	}
	vAddr, err := sdk.AccAddressFromBech32(v.Validator)
	if err != nil {
		return err
	}
	return s.Validations.Set(ctx, validationKey(tAddr, v.PocStageStartBlockHeight, v.ModelId, vAddr), v)
}

func (s *Store) ListValidationsForSegment(ctx context.Context, target string, start int64) ([]types.PoCChallengeValidation, error) {
	addr, err := sdk.AccAddressFromBech32(target)
	if err != nil {
		return nil, err
	}
	rng := collections.NewSuperPrefixedQuadRange[sdk.AccAddress, int64, string, sdk.AccAddress](addr, start)
	iter, err := s.Validations.Iterate(ctx, rng)
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	return iter.Values()
}

func (s *Store) DeleteChallengeData(ctx context.Context, ch types.PoCChallenge) error {
	addr, err := sdk.AccAddressFromBech32(ch.Target)
	if err != nil {
		return err
	}
	commitIter, err := s.Commits.Iterate(ctx, collections.NewPrefixedQuadRange[sdk.AccAddress, int64, string, uint32](addr))
	if err != nil {
		return err
	}
	var commitKeys []collections.Quad[sdk.AccAddress, int64, string, uint32]
	for ; commitIter.Valid(); commitIter.Next() {
		key, err := commitIter.Key()
		if err != nil {
			commitIter.Close()
			return err
		}
		commitKeys = append(commitKeys, key)
	}
	commitIter.Close()
	for _, key := range commitKeys {
		if err := s.Commits.Remove(ctx, key); err != nil {
			return err
		}
	}

	valIter, err := s.Validations.Iterate(ctx, collections.NewPrefixedQuadRange[sdk.AccAddress, int64, string, sdk.AccAddress](addr))
	if err != nil {
		return err
	}
	var valKeys []collections.Quad[sdk.AccAddress, int64, string, sdk.AccAddress]
	for ; valIter.Valid(); valIter.Next() {
		key, err := valIter.Key()
		if err != nil {
			valIter.Close()
			return err
		}
		valKeys = append(valKeys, key)
	}
	valIter.Close()
	for _, key := range valKeys {
		if err := s.Validations.Remove(ctx, key); err != nil {
			return err
		}
	}
	return s.Challenges.Remove(ctx, addr)
}
