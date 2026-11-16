// Package lightproof verifies carried mainnet light blocks against a
// consensus-pinned checkpoint. It has no dependency on devshard or warm keys.
package lightproof

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	tmtypes "github.com/cometbft/cometbft/types"
)

const (
	MaxBlocks     = 128
	MaxBlockBytes = 512 * 1024
	MaxProofBytes = 8 * 1024 * 1024
)

var (
	ErrInvalid      = errors.New("invalid mainnet light proof")
	ErrIntermediate = errors.New("light proof requires a validator transition checkpoint")
)

// Checkpoint is the consensus-committed trust state. A bootstrap checkpoint
// contains only Height and Hash, obtained from the chain escrow creation.
// Once verified, the other fields come exclusively from the authenticated
// header. Hash and NextValidatorsHash are copied on every returned value.
type Checkpoint struct {
	Height             int64
	Hash               []byte
	NextValidatorsHash []byte
	Time               time.Time
	ChainID            string
}

type Source interface {
	LightBlockAt(context.Context, int64) ([]byte, error)
}

func validateCheckpoint(cp Checkpoint) error {
	if cp.Height <= 0 || len(cp.Hash) != 32 {
		return fmt.Errorf("%w: missing pinned checkpoint", ErrInvalid)
	}
	if len(cp.NextValidatorsHash) != 0 && (len(cp.NextValidatorsHash) != 32 || cp.ChainID == "" || cp.Time.IsZero()) {
		return fmt.Errorf("%w: incomplete verified checkpoint", ErrInvalid)
	}
	return nil
}

func Decode(raw []byte) (*tmtypes.LightBlock, error) {
	if len(raw) == 0 || len(raw) > MaxBlockBytes {
		return nil, fmt.Errorf("%w: light block size %d", ErrInvalid, len(raw))
	}
	var pb tmproto.LightBlock
	if err := pb.Unmarshal(raw); err != nil {
		return nil, fmt.Errorf("%w: decode: %v", ErrInvalid, err)
	}
	// CometBFT conversion permits missing nested messages. Check these before
	// accessing the embedded header's ChainID or validating the commit.
	if pb.SignedHeader == nil || pb.SignedHeader.Header == nil || pb.SignedHeader.Commit == nil || pb.ValidatorSet == nil {
		return nil, fmt.Errorf("%w: incomplete light block", ErrInvalid)
	}
	lb, err := tmtypes.LightBlockFromProto(&pb)
	if err != nil {
		return nil, fmt.Errorf("%w: light block: %v", ErrInvalid, err)
	}
	if err := lb.ValidateBasic(lb.ChainID); err != nil {
		return nil, fmt.Errorf("%w: header: %v", ErrInvalid, err)
	}
	if lb.Time.IsZero() || !time.Unix(0, lb.Time.UnixNano()).Equal(lb.Time) {
		return nil, fmt.Errorf("%w: block time cannot be represented in Unix nanoseconds", ErrInvalid)
	}
	if err := lb.ValidatorSet.VerifyCommitLight(lb.ChainID, lb.Commit.BlockID, lb.Height, lb.Commit); err != nil {
		return nil, fmt.Errorf("%w: commit: %v", ErrInvalid, err)
	}
	return lb, nil
}

func checkpoint(lb *tmtypes.LightBlock) Checkpoint {
	return Checkpoint{Height: lb.Height, Hash: bytes.Clone(lb.Hash()), NextValidatorsHash: bytes.Clone(lb.NextValidatorsHash), Time: lb.Time, ChainID: lb.ChainID}
}

// advance accepts a commit only from the exact validator set authenticated by
// the preceding checkpoint. Skipping unchanged sets is safe: the same known
// >2/3 voting power directly signed the target header. A changing set requires
// an intermediate header that commits to the new set. No peer-provided set
// can establish its own authority by signing its own header.
func advance(from Checkpoint, lb *tmtypes.LightBlock) (Checkpoint, error) {
	if from.Height <= 0 || len(from.Hash) != 32 {
		return Checkpoint{}, fmt.Errorf("%w: missing pinned checkpoint", ErrInvalid)
	}
	if len(from.NextValidatorsHash) == 0 {
		if lb.Height != from.Height || !bytes.Equal(lb.Hash(), from.Hash) {
			return Checkpoint{}, fmt.Errorf("%w: bootstrap does not match the pinned block", ErrInvalid)
		}
		return checkpoint(lb), nil
	}
	if lb.ChainID != from.ChainID || lb.Height <= from.Height || !lb.Time.After(from.Time) {
		return Checkpoint{}, fmt.Errorf("%w: chain, height or time regressed", ErrInvalid)
	}
	if !bytes.Equal(lb.ValidatorSet.Hash(), from.NextValidatorsHash) {
		return Checkpoint{}, ErrIntermediate
	}
	if lb.Height == from.Height+1 && !bytes.Equal(lb.LastBlockID.Hash, from.Hash) {
		return Checkpoint{}, fmt.Errorf("%w: adjacent parent hash mismatch", ErrInvalid)
	}
	return checkpoint(lb), nil
}

// Verify is deterministic on replay: it uses only the pinned checkpoint and
// carried signed headers, never a wall clock, a mutable local header map, or a
// later validator set. An error returns no partially advanced checkpoint.
func Verify(from Checkpoint, target int64, proof [][]byte) (Checkpoint, error) {
	if err := validateCheckpoint(from); err != nil {
		return Checkpoint{}, err
	}
	if target < from.Height || target <= 0 || len(proof) > MaxBlocks {
		return Checkpoint{}, fmt.Errorf("%w: target or proof length", ErrInvalid)
	}
	cur := from
	cur.Hash = bytes.Clone(cur.Hash)
	cur.NextValidatorsHash = bytes.Clone(cur.NextValidatorsHash)
	total := 0
	for _, raw := range proof {
		total += len(raw)
		if total > MaxProofBytes {
			return Checkpoint{}, fmt.Errorf("%w: proof too large", ErrInvalid)
		}
		lb, err := Decode(raw)
		if err != nil {
			return Checkpoint{}, err
		}
		cur, err = advance(cur, lb)
		if err != nil {
			return Checkpoint{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	if cur.Height != target || len(cur.NextValidatorsHash) == 0 {
		return Checkpoint{}, fmt.Errorf("%w: target not established", ErrInvalid)
	}
	return cur, nil
}

// Build bisects validator changes instead of trusting a replacement set or
// downloading every unchanged block. The consumer still verifies the entire
// returned proof. Both work and output size are bounded.
func Build(ctx context.Context, from Checkpoint, target int64, source Source) ([][]byte, Checkpoint, error) {
	if err := validateCheckpoint(from); err != nil {
		return nil, Checkpoint{}, err
	}
	if source == nil || target < from.Height || from.Height <= 0 {
		return nil, Checkpoint{}, fmt.Errorf("%w: source or target", ErrInvalid)
	}
	var proof [][]byte
	cache := make(map[int64][]byte)
	total, reads, fetched := 0, 0, 0
	read := func(height int64) ([]byte, *tmtypes.LightBlock, error) {
		raw, ok := cache[height]
		if !ok {
			if reads >= MaxBlocks*16 {
				return nil, nil, fmt.Errorf("%w: proof fetch budget", ErrInvalid)
			}
			reads++
			var err error
			raw, err = source.LightBlockAt(ctx, height)
			if err != nil {
				return nil, nil, err
			}
			fetched += len(raw)
			if fetched > MaxProofBytes*2 {
				return nil, nil, fmt.Errorf("%w: proof fetch byte budget", ErrInvalid)
			}
			raw = bytes.Clone(raw)
			cache[height] = raw
		}
		lb, err := Decode(raw)
		if err == nil && lb.Height != height {
			err = fmt.Errorf("%w: source returned height %d for %d", ErrInvalid, lb.Height, height)
		}
		return raw, lb, err
	}
	appendBlock := func(raw []byte) error {
		total += len(raw)
		if len(proof) >= MaxBlocks || total > MaxProofBytes {
			return fmt.Errorf("%w: proof budget", ErrInvalid)
		}
		proof = append(proof, bytes.Clone(raw))
		return nil
	}
	cur := from
	cur.Hash = bytes.Clone(cur.Hash)
	cur.NextValidatorsHash = bytes.Clone(cur.NextValidatorsHash)
	if len(cur.NextValidatorsHash) == 0 {
		raw, lb, err := read(cur.Height)
		if err != nil {
			return nil, Checkpoint{}, err
		}
		cur, err = advance(cur, lb)
		if err != nil {
			return nil, Checkpoint{}, err
		}
		if err := appendBlock(raw); err != nil {
			return nil, Checkpoint{}, err
		}
	}
	var walk func(Checkpoint, int64) (Checkpoint, error)
	walk = func(cp Checkpoint, to int64) (Checkpoint, error) {
		if err := ctx.Err(); err != nil {
			return Checkpoint{}, err
		}
		if cp.Height == to {
			return cp, nil
		}
		raw, lb, err := read(to)
		if err != nil {
			return Checkpoint{}, err
		}
		next, err := advance(cp, lb)
		if errors.Is(err, ErrIntermediate) && to > cp.Height+1 {
			mid, err := walk(cp, cp.Height+(to-cp.Height)/2)
			if err != nil {
				return Checkpoint{}, err
			}
			return walk(mid, to)
		}
		if err != nil {
			return Checkpoint{}, err
		}
		return next, appendBlock(raw)
	}
	next, err := walk(cur, target)
	if err != nil {
		return nil, Checkpoint{}, err
	}
	return proof, next, nil
}
