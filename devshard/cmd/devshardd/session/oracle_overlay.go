package session

import (
	"context"
	"strconv"
	"strings"

	"common/chainoracle/blocks"
)

const (
	envTestenvOracleHeightDelta   = "DEVSHARD_TESTENV_ORACLE_HEIGHT_DELTA"
	envTestenvOracleFabricateHash = "DEVSHARD_TESTENV_ORACLE_FABRICATE_HASH"
)

// parseOracleOverlay reads the testenv oracle knobs. A zero delta and a
// false fabricate flag mean "do not wrap". Invalid delta strings are ignored.
func parseOracleOverlay(deltaRaw, fabRaw string) (delta int64, fabricate bool) {
	if raw := strings.TrimSpace(deltaRaw); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			delta = n
		}
	}
	switch strings.ToLower(strings.TrimSpace(fabRaw)) {
	case "1", "true", "on", "yes":
		fabricate = true
	}
	return delta, fabricate
}

// wrapTestenvOracleOverlay shifts Latest() (and only Latest) so citest can
// make one host lag or claim a future / fabricated tip without a second
// chain. At/Prove/Subscribe stay canonical so L6 reconciliation still sees
// the real header once the follower reaches the claimed height.
func wrapTestenvOracleOverlay(inner blocks.BlockOracle, delta int64, fabricate bool) blocks.BlockOracle {
	if inner == nil || (delta == 0 && !fabricate) {
		return inner
	}
	return &testenvOracleOverlay{inner: inner, delta: delta, fabricate: fabricate}
}

type testenvOracleOverlay struct {
	inner     blocks.BlockOracle
	delta     int64
	fabricate bool
}

func (o *testenvOracleOverlay) Latest(ctx context.Context) (*blocks.Header, error) {
	h, err := o.inner.Latest(ctx)
	if err != nil || h == nil {
		return h, err
	}
	return shiftTestenvHeader(h, o.delta, o.fabricate), nil
}

func (o *testenvOracleOverlay) At(ctx context.Context, height int64) (*blocks.Header, error) {
	return o.inner.At(ctx, height)
}

func (o *testenvOracleOverlay) Prove(ctx context.Context, path string, height int64) (*blocks.Proof, error) {
	return o.inner.Prove(ctx, path, height)
}

func (o *testenvOracleOverlay) Subscribe(ctx context.Context, fromHeight int64) (<-chan *blocks.Header, error) {
	return o.inner.Subscribe(ctx, fromHeight)
}

func shiftTestenvHeader(h *blocks.Header, delta int64, fabricate bool) *blocks.Header {
	out := *h
	out.BlockHash = append([]byte(nil), h.BlockHash...)
	out.Height += delta
	if out.Height < 1 {
		out.Height = 1
	}
	if fabricate && len(out.BlockHash) > 0 {
		out.BlockHash[0] ^= 0xff
	}
	return &out
}
