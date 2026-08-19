package observer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"devshard/chainoracle/blocks"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
)

// TendermintConfig pins the hash-only observer's CometBFT RPC endpoint.
type TendermintConfig struct {
	ChainID    string
	RPCURL     string
	PollPeriod string // e.g. "500ms"; default 1s
}

type blockRPC interface {
	Block(ctx context.Context, height *int64) (*ctypes.ResultBlock, error)
}

// Tendermint is a hash-only BlockOracle that polls CometBFT RPC.
type Tendermint struct {
	rpc    blockRPC
	period time.Duration

	mu      sync.RWMutex
	latest  *blocks.Header
	history map[int64]*blocks.Header
	subs    map[int]chan *blocks.Header
	nextSub int

	cancel context.CancelFunc
	done   chan struct{}
}

// NewTendermint starts a background poll loop against cfg.RPCURL.
// Headers are hash-only (empty Commit). Cancel ctx to stop.
func NewTendermint(ctx context.Context, cfg TendermintConfig) (Observer, error) {
	if cfg.RPCURL == "" {
		return nil, errors.New("observer: empty RPC URL")
	}
	period := time.Second
	if cfg.PollPeriod != "" {
		d, err := time.ParseDuration(cfg.PollPeriod)
		if err != nil {
			return nil, fmt.Errorf("observer: poll period: %w", err)
		}
		if d > 0 {
			period = d
		}
	}
	rpc, err := rpchttp.New(cfg.RPCURL, "/websocket")
	if err != nil {
		return nil, fmt.Errorf("observer: tendermint rpc: %w", err)
	}
	return startTendermint(ctx, rpc, period)
}

func startTendermint(ctx context.Context, rpc blockRPC, period time.Duration) (*Tendermint, error) {
	runCtx, cancel := context.WithCancel(ctx)
	o := &Tendermint{
		rpc:     rpc,
		period:  period,
		history: make(map[int64]*blocks.Header),
		subs:    make(map[int]chan *blocks.Header),
		cancel:  cancel,
		done:    make(chan struct{}),
	}
	go func() {
		defer close(o.done)
		_ = o.Run(runCtx)
	}()
	return o, nil
}

// Run polls until ctx is cancelled. NewTendermint already starts this.
func (o *Tendermint) Run(ctx context.Context) error {
	if err := o.pollOnce(ctx); err != nil && ctx.Err() == nil {
		// first miss is not fatal; keep polling
	}
	tick := time.NewTicker(o.period)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			_ = o.pollOnce(ctx)
		}
	}
}

func (o *Tendermint) pollOnce(ctx context.Context) error {
	res, err := o.rpc.Block(ctx, nil)
	if err != nil {
		return err
	}
	hdr, err := HeaderFromResultBlock(res)
	if err != nil {
		return err
	}
	o.store(hdr)
	return nil
}

func (o *Tendermint) store(hdr *blocks.Header) {
	if hdr == nil {
		return
	}
	cp := *hdr
	cp.BlockHash = append([]byte(nil), hdr.BlockHash...)
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.latest != nil && hdr.Height <= o.latest.Height {
		return
	}
	o.latest = &cp
	o.history[hdr.Height] = &cp
	for _, ch := range o.subs {
		select {
		case ch <- cloneHashOnly(&cp):
		default:
		}
	}
}

func (o *Tendermint) Latest(ctx context.Context) (*blocks.Header, error) {
	o.mu.RLock()
	h := o.latest
	o.mu.RUnlock()
	if h != nil {
		return cloneHashOnly(h), nil
	}
	if err := o.pollOnce(ctx); err != nil {
		return nil, err
	}
	o.mu.RLock()
	h = o.latest
	o.mu.RUnlock()
	if h == nil {
		return nil, errors.New("observer: no header")
	}
	return cloneHashOnly(h), nil
}

func (o *Tendermint) At(ctx context.Context, height int64) (*blocks.Header, error) {
	o.mu.RLock()
	if h, ok := o.history[height]; ok {
		o.mu.RUnlock()
		return cloneHashOnly(h), nil
	}
	o.mu.RUnlock()
	res, err := o.rpc.Block(ctx, &height)
	if err != nil {
		return nil, err
	}
	hdr, err := HeaderFromResultBlock(res)
	if err != nil {
		return nil, err
	}
	o.store(hdr)
	return cloneHashOnly(hdr), nil
}

func (o *Tendermint) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}

func (o *Tendermint) Subscribe(ctx context.Context, fromHeight int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header, 16)
	o.mu.Lock()
	id := o.nextSub
	o.nextSub++
	o.subs[id] = ch
	var replay []*blocks.Header
	if o.latest != nil {
		for h := fromHeight; h <= o.latest.Height; h++ {
			if v, ok := o.history[h]; ok {
				replay = append(replay, cloneHashOnly(v))
			}
		}
	}
	o.mu.Unlock()
	go func() {
		defer func() {
			o.mu.Lock()
			delete(o.subs, id)
			o.mu.Unlock()
			close(ch)
		}()
		for _, h := range replay {
			select {
			case <-ctx.Done():
				return
			case ch <- h:
			}
		}
		<-ctx.Done()
	}()
	return ch, nil
}

func cloneHashOnly(h *blocks.Header) *blocks.Header {
	if h == nil {
		return nil
	}
	cp := *h
	cp.BlockHash = append([]byte(nil), h.BlockHash...)
	return &cp
}
