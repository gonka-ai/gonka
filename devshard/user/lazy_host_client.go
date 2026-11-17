package user

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"devshard/host"
	"devshard/transport"
	"devshard/types"
)

// ErrHostUnresolved is a call to a host whose client could not be built yet
// because its inference URL lookup failed or has not answered.
var ErrHostUnresolved = errors.New("host client unresolved")

// unresolvedHostChatWait is the ChatBudgetWait an unresolved host reports. It
// is well past the gateway's spent-budget threshold, so the picker leaves the
// host out instead of signing a start it cannot deliver.
const unresolvedHostChatWait = time.Second

// lazyHostClient stands in for a host that was not resolved when the session
// opened. Calls that need the host build its client on first use; accessors
// answer empty until it exists. Every optional client interface the session
// and gateway probe for is forwarded once the client is built.
type lazyHostClient struct {
	lazy *transport.Lazy[HostClient]
	// view is a WithoutAdmission copy: it strips admission from the built
	// client per call and does not own the shared Lazy.
	view bool
}

func newLazyHostClient(lazy *transport.Lazy[HostClient]) *lazyHostClient {
	return &lazyHostClient{lazy: lazy}
}

func releaseHostClient(c HostClient) {
	if closer, ok := c.(interface{ Close() }); ok {
		closer.Close()
	}
}

// client builds or returns the host client. done releases the per-call
// admission-stripped copy a view makes; call it once the call returns.
func (c *lazyHostClient) client(ctx context.Context) (hc HostClient, done func(), err error) {
	hc, err = c.lazy.Get(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrHostUnresolved, err)
	}
	if c.view {
		if b, ok := hc.(interface{ WithoutAdmission() any }); ok {
			if stripped, ok := b.WithoutAdmission().(HostClient); ok {
				return stripped, func() { releaseHostClient(stripped) }, nil
			}
		}
	}
	return hc, func() {}, nil
}

func (c *lazyHostClient) built() HostClient {
	hc, ok := c.lazy.Peek()
	if !ok {
		return nil
	}
	return hc
}

func (c *lazyHostClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	hc, done, err := c.client(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return hc.Send(ctx, req, stream, receiptHandler)
}

func (c *lazyHostClient) GetSignatures(ctx context.Context, nonce uint64) (map[uint32][]byte, error) {
	hc, done, err := c.client(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	f, ok := hc.(SignatureFetcher)
	if !ok {
		return nil, fmt.Errorf("%T does not fetch signatures", hc)
	}
	return f.GetSignatures(ctx, nonce)
}

func (c *lazyHostClient) VerifyTimeout(ctx context.Context, inferenceID uint64, reason types.TimeoutReason, payload *host.InferencePayload, diffs []types.Diff, artifacts host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	hc, done, err := c.client(ctx)
	if err != nil {
		return false, nil, 0, nil, "", err
	}
	defer done()
	tv, ok := hc.(TimeoutVerifier)
	if !ok {
		return false, nil, 0, nil, "", fmt.Errorf("%T does not verify timeouts", hc)
	}
	return tv.VerifyTimeout(ctx, inferenceID, reason, payload, diffs, artifacts)
}

func (c *lazyHostClient) VerifyErrorMiss(ctx context.Context, inferenceID uint64, diffs []types.Diff, artifacts host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	hc, done, err := c.client(ctx)
	if err != nil {
		return false, nil, 0, nil, "", err
	}
	defer done()
	tv, ok := hc.(TimeoutVerifier)
	if !ok {
		return false, nil, 0, nil, "", fmt.Errorf("%T does not verify timeouts", hc)
	}
	return tv.VerifyErrorMiss(ctx, inferenceID, diffs, artifacts)
}

func (c *lazyHostClient) VerifyChallenge(ctx context.Context, inferenceID uint64, reason types.ChallengeReason, diffs []types.Diff) (*transport.VerifyChallengeResponse, error) {
	hc, done, err := c.client(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	cv, ok := hc.(ChallengeVerifier)
	if !ok {
		return nil, fmt.Errorf("%T does not verify challenges", hc)
	}
	return cv.VerifyChallenge(ctx, inferenceID, reason, diffs)
}

func (c *lazyHostClient) SeedHeightSync(ctx context.Context) (bool, error) {
	hc, done, err := c.client(ctx)
	if err != nil {
		return false, err
	}
	defer done()
	s, ok := hc.(heightSyncSeeder)
	if !ok {
		return false, fmt.Errorf("%T does not seed height sync", hc)
	}
	return s.SeedHeightSync(ctx)
}

// WithoutAdmission returns a view that strips admission from the client once
// it is built. Closing the view leaves the shared client open.
func (c *lazyHostClient) WithoutAdmission() any {
	if hc := c.built(); hc != nil {
		if b, ok := hc.(interface{ WithoutAdmission() any }); ok {
			return b.WithoutAdmission()
		}
		return hc
	}
	return &lazyHostClient{lazy: c.lazy, view: true}
}

// ChatBudgetWait forwards the built client's budget. Unresolved, it starts a
// lookup when the retry window allows and reports the host as spent.
func (c *lazyHostClient) ChatBudgetWait() time.Duration {
	if hc := c.built(); hc != nil {
		if b, ok := hc.(interface{ ChatBudgetWait() time.Duration }); ok {
			return b.ChatBudgetWait()
		}
		return 0
	}
	c.lazy.Start()
	return unresolvedHostChatWait
}

func (c *lazyHostClient) CatalogHealthzURL() string {
	if src, ok := c.built().(catalogHealthzSource); ok {
		return src.CatalogHealthzURL()
	}
	return ""
}

func (c *lazyHostClient) ObservedStampNow() (uint64, []byte, bool) {
	if src, ok := c.built().(interface {
		ObservedStampNow() (uint64, []byte, bool)
	}); ok {
		return src.ObservedStampNow()
	}
	return 0, nil, false
}

func (c *lazyHostClient) ObservedHeightNow() (uint64, bool) {
	if src, ok := c.built().(interface{ ObservedHeightNow() (uint64, bool) }); ok {
		return src.ObservedHeightNow()
	}
	return 0, false
}

func (c *lazyHostClient) HeightSyncPeerTips() *transport.HeightSyncPeerTips {
	if src, ok := c.built().(interface {
		HeightSyncPeerTips() *transport.HeightSyncPeerTips
	}); ok {
		return src.HeightSyncPeerTips()
	}
	return nil
}

func (c *lazyHostClient) BaseURL() string {
	if d, ok := c.built().(interface{ BaseURL() string }); ok {
		return d.BaseURL()
	}
	return ""
}

func (c *lazyHostClient) RoutePrefix() string {
	if d, ok := c.built().(interface{ RoutePrefix() string }); ok {
		return d.RoutePrefix()
	}
	return ""
}

func (c *lazyHostClient) Close() {
	if !c.view {
		c.lazy.Close()
	}
}
