package store

import (
	"context"
	"fmt"
	"time"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/lightproof"
	mockoracle "devshard/chainoracle/blocks/observer"
	"devshard/signing"
	tmtypes "github.com/cometbft/cometbft/types"
)

// EnableMainnetProofs gives the stand one signed chain for RPC, gRPC and block
// events across protocol versions. V7 escrows also pin an anchor from it.
func (s *Store) EnableMainnetProofs(interval time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mainnet != nil {
		return nil
	}
	key, err := signing.GenerateKey()
	if err != nil {
		return err
	}
	addr, err := blocks.AddressBytes(key.CompressedPublicKeyBytes())
	if err != nil {
		return err
	}
	oracle, err := mockoracle.NewMock(mockoracle.MockConfig{LightBlocks: true, ChainID: s.ChainID, Validators: []mockoracle.MockValidator{{Signer: key, Address: addr, Power: 100}}, InitialHeight: max(1, s.BlockHeight), Start: time.Now().UTC(), BlockInterval: interval})
	if err != nil {
		return err
	}
	first, err := oracle.AdvanceOne()
	if err != nil {
		return err
	}
	s.mainnet = oracle
	if s.MainnetProofs {
		for _, escrow := range s.Escrows {
			escrow.MainnetAnchorHeight = first.Height
			escrow.MainnetAnchorHash = append([]byte(nil), first.BlockHash...)
		}
	}
	return nil
}

func (s *Store) mainnetAtLocked(ctx context.Context, height int64) (*blocks.Header, error) {
	if s.mainnet == nil {
		return nil, blocks.ErrProveNotImplemented
	}
	if height <= 0 || height > s.BlockHeight {
		return nil, blocks.ErrHeaderNotFound
	}
	tip, err := s.mainnet.Latest(ctx)
	if err != nil {
		return nil, err
	}
	for tip.Height < height {
		tip, err = s.mainnet.AdvanceOne()
		if err != nil {
			return nil, err
		}
	}
	return s.mainnet.At(ctx, height)
}

func (s *Store) MainnetLightBlock(ctx context.Context, height int64) (*tmtypes.LightBlock, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if height == 0 {
		height = s.BlockHeight
	}
	if _, err := s.mainnetAtLocked(ctx, height); err != nil {
		return nil, err
	}
	raw, err := s.mainnet.LightBlockAt(ctx, height)
	if err != nil {
		return nil, err
	}
	return lightproof.Decode(raw)
}

type mainnetOracle struct{ s *Store }

func (s *Store) MainnetOracle() blocks.BlockOracle { return mainnetOracle{s} }
func (o mainnetOracle) Latest(ctx context.Context) (*blocks.Header, error) {
	return o.At(ctx, o.s.GetBlockHeight())
}
func (o mainnetOracle) At(ctx context.Context, h int64) (*blocks.Header, error) {
	o.s.mu.Lock()
	defer o.s.mu.Unlock()
	return o.s.mainnetAtLocked(ctx, h)
}
func (o mainnetOracle) LightBlockAt(ctx context.Context, h int64) ([]byte, error) {
	o.s.mu.Lock()
	defer o.s.mu.Unlock()
	if _, err := o.s.mainnetAtLocked(ctx, h); err != nil {
		return nil, err
	}
	return o.s.mainnet.LightBlockAt(ctx, h)
}
func (mainnetOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}
func (mainnetOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	return nil, fmt.Errorf("mock chain: use Comet events")
}
