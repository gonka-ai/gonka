package mockdapi

import (
	"context"
	"errors"

	"devshard/blockoracle"
	"devshard/blockoracle/client"
)

// newOracleClient builds a blockoracle HTTP/SSE client in host-trust
// mode (Verifier = nil) pointed at cfg.HeightSyncURL. The returned
// client is already subscribed; teardown happens when its background
// goroutine sees the supplied context cancelled (see MockDapi.Close).
//
// Host-trust mode is load-bearing: devshardd hosts in the testenv (and
// in production, via in-process dapi wiring) trust the oracle by
// construction and cache every incoming header — including the full
// Commit.Signatures vector — so downstream settlement can forward
// multi-sig proofs without a second round trip. Non-host consumers
// (devshardctl) build their own client with a pinned Verifier; see
// devshard/docs/testenv.md §3.5 and §5.
func newOracleClient(ctx context.Context, cfg Config) (*client.Client, error) {
	if cfg.HeightSyncURL == "" {
		return nil, errors.New("mockdapi: empty HeightSyncURL")
	}
	return client.NewHTTP(ctx, client.HTTPConfig{
		BaseURL:          cfg.HeightSyncURL,
		Verifier:         nil, // host-trust mode; see doc comment above
		HTTPClient:       cfg.HTTPClient,
		SubscribeFrom:    0, // subscribe from latest
		ResubscribeAfter: cfg.ResubscribeAfter,
		StaleAfter:       cfg.StaleAfter,
	})
}

// compile-time assertion that the client satisfies the consumer contract.
var _ blockoracle.BlockOracle = (*client.Client)(nil)
