package observer

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/tipcache"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
)

// TendermintConfig pins the hash-only oracle to a CometBFT RPC endpoint.
// PollPeriod is ignored: tip motion is Observe (the existing dapi NewBlock
// listener), not a 1s Block poll.
type TendermintConfig struct {
	ChainID    string
	RPCURL     string
	PollPeriod string // ignored; kept so callers do not have to drop the field
}

type headerRPC interface {
	Header(ctx context.Context, height *int64) (*ctypes.ResultHeader, error)
}

// Oracle is a hash-only BlockOracle. Tip is pushed via Observe (Comet
// NewBlock). At() is the last tipcache.HistoryWindow heights, then Comet
// Header RPC (LoadBlockMeta — not Block / LoadBlock).
type Oracle struct {
	rpc   headerRPC
	cache *tipcache.Cache
}

// NewTendermint builds an Oracle against cfg.RPCURL. It does not start a
// poll loop. Cancel is not required; stop feeding Observe when shutting down.
func NewTendermint(cfg TendermintConfig) (*Oracle, error) {
	if cfg.RPCURL == "" {
		return nil, errors.New("observer: empty RPC URL")
	}
	rpc, err := rpchttp.New(cfg.RPCURL, "/websocket")
	if err != nil {
		return nil, fmt.Errorf("observer: tendermint rpc: %w", err)
	}
	return newOracle(rpc), nil
}

func newOracle(rpc headerRPC) *Oracle {
	return &Oracle{
		rpc:   rpc,
		cache: tipcache.New(0),
	}
}

// Observe records a committed header from the dapi NewBlock listener.
func (o *Oracle) Observe(h *blocks.Header) {
	if o == nil {
		return
	}
	o.cache.Observe(h)
}

// ObserveHex decodes a hex block hash (Comet WS block_id.hash) and Observes.
func (o *Oracle) ObserveHex(height int64, hashHex string, t time.Time, chainID string) error {
	if o == nil {
		return nil
	}
	hash, err := decodeBlockHash(hashHex)
	if err != nil {
		return err
	}
	o.Observe(blocks.HashOnlyHeader(height, t, chainID, hash))
	return nil
}

func (o *Oracle) Latest(ctx context.Context) (*blocks.Header, error) {
	if o == nil {
		return nil, blocks.ErrHeaderNotFound
	}
	h, err := o.cache.Latest(ctx)
	if err == nil && h != nil {
		return h, nil
	}
	return o.fetchHeader(ctx, nil, true)
}

func (o *Oracle) At(ctx context.Context, height int64) (*blocks.Header, error) {
	if o == nil {
		return nil, blocks.ErrHeaderNotFound
	}
	h, err := o.cache.At(ctx, height)
	if err == nil && h != nil {
		return h, nil
	}
	return o.fetchHeader(ctx, &height, false)
}

func (o *Oracle) fetchHeader(ctx context.Context, height *int64, observe bool) (*blocks.Header, error) {
	res, err := o.rpc.Header(ctx, height)
	if err != nil {
		if isCometHeightErr(err) {
			return nil, blocks.ErrHeaderNotFound
		}
		return nil, fmt.Errorf("observer: header rpc: %w", err)
	}
	hdr, err := HeaderFromResultHeader(res)
	if err != nil {
		return nil, err
	}
	if observe {
		o.cache.Observe(hdr)
	} else {
		o.cache.Remember(hdr)
	}
	return hdr, nil
}

func (o *Oracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}

func (o *Oracle) Subscribe(ctx context.Context, fromHeight int64) (<-chan *blocks.Header, error) {
	if o == nil {
		ch := make(chan *blocks.Header)
		close(ch)
		return ch, nil
	}
	return o.cache.Subscribe(ctx, fromHeight)
}

func decodeBlockHash(raw string) ([]byte, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "0x")
	s = strings.TrimPrefix(s, "0X")
	if s == "" {
		return nil, fmt.Errorf("observer: empty block hash")
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("observer: block hash: %w", err)
	}
	return b, nil
}

func isCometHeightErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "is not available"):
		return true
	case strings.Contains(msg, "must be less than or equal"):
		return true
	case strings.Contains(msg, "height must be greater"):
		return true
	default:
		return false
	}
}

var (
	_ blocks.BlockOracle = (*Oracle)(nil)
	_ Observer           = (*Oracle)(nil)
)
