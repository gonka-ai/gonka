// Package client is the unary HTTP consumer of GET /block/:height and
// GET /block/:height/prove. The live tip is Comet NewBlock (tipcache), not
// this package.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"common/chainoracle/blocks"
	"devshard/chainoracle/blocks/verifier"
)

// StatusError is a non-200 HTTP response from the chainoracle wire protocol.
type StatusError struct {
	URL        string
	StatusCode int
}

func (e *StatusError) Error() string {
	if e == nil {
		return "blockoracle/client: status error"
	}
	return fmt.Sprintf("blockoracle/client: GET %s: status %d", e.URL, e.StatusCode)
}

// IsCapabilityMiss reports a missing /block/* route (old dapi) rather than a
// transport failure. 501 on Prove is not a capability miss for At.
func IsCapabilityMiss(err error) bool {
	var se *StatusError
	if !errors.As(err, &se) || se == nil {
		return false
	}
	switch se.StatusCode {
	case http.StatusNotFound, http.StatusNotImplemented, http.StatusGone:
		return true
	default:
		return false
	}
}

// HTTPConfig pins the unary lookup client to a producer. Verifier is optional:
// nil trusts the producer (host/gateway). Auditors should set a Verifier.
type HTTPConfig struct {
	BaseURL    string
	Verifier   *verifier.Verifier
	HTTPClient *http.Client
}

// Lookup is unary GET /block/:height and GET /block/:height/prove.
type Lookup struct {
	cfg   HTTPConfig
	unary *http.Client
}

// NewLookup builds a unary client. A missing /block/:height (old dapi)
// returns DummyHeader rather than an error so L6 stays quiet.
func NewLookup(cfg HTTPConfig) (*Lookup, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("blockoracle/client: empty base url")
	}
	if _, err := url.Parse(cfg.BaseURL); err != nil {
		return nil, fmt.Errorf("blockoracle/client: invalid base url: %w", err)
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	} else if hc.Timeout == 0 {
		cp := *hc
		cp.Timeout = 10 * time.Second
		hc = &cp
	}
	return &Lookup{
		cfg:   cfg,
		unary: hc,
	}, nil
}

func (l *Lookup) Close() {}

func (l *Lookup) At(ctx context.Context, height int64) (*blocks.Header, error) {
	if l == nil {
		return blocks.DummyHeader(height), nil
	}
	h, err := l.fetchAt(ctx, height)
	if err != nil {
		if IsCapabilityMiss(err) {
			return blocks.DummyHeader(height), nil
		}
		return nil, err
	}
	return h, nil
}

func (l *Lookup) Prove(ctx context.Context, path string, height int64) (*blocks.Proof, error) {
	if l == nil {
		return nil, blocks.ErrProveNotImplemented
	}
	u, err := l.joinURL(fmt.Sprintf("/block/%d/prove", height))
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("path", path)
	u.RawQuery = q.Encode()

	payload, err := l.get(ctx, u.String())
	if err != nil {
		var se *StatusError
		if errors.As(err, &se) && se != nil && se.StatusCode == http.StatusNotImplemented {
			return nil, blocks.ErrProveNotImplemented
		}
		return nil, err
	}
	var proof blocks.Proof
	if err := json.Unmarshal(payload, &proof); err != nil {
		return nil, fmt.Errorf("blockoracle/client: decode proof: %w", err)
	}
	return &proof, nil
}

func (l *Lookup) Latest(context.Context) (*blocks.Header, error) {
	return nil, fmt.Errorf("blockoracle/client: lookup has no /block/latest; use the Comet tip")
}

func (l *Lookup) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}

func (l *Lookup) fetchAt(ctx context.Context, height int64) (*blocks.Header, error) {
	u, err := l.joinURL(fmt.Sprintf("/block/%d", height))
	if err != nil {
		return nil, err
	}
	payload, err := l.get(ctx, u.String())
	if err != nil {
		return nil, err
	}
	var h blocks.Header
	if err := json.Unmarshal(payload, &h); err != nil {
		return nil, fmt.Errorf("blockoracle/client: decode at: %w", err)
	}
	if l.cfg.Verifier != nil {
		if err := l.cfg.Verifier.Verify(&h, 0); err != nil {
			return nil, fmt.Errorf("blockoracle/client: verify at: %w", err)
		}
	}
	return &h, nil
}

func (l *Lookup) get(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := l.unary.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{URL: rawURL, StatusCode: resp.StatusCode}
	}
	return io.ReadAll(resp.Body)
}

func (l *Lookup) joinURL(path string) (*url.URL, error) {
	u, err := url.Parse(l.cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	return u, nil
}

// NewInProcess returns a BlockOracle backed by an in-process oracle.
func NewInProcess(o blocks.BlockOracle) blocks.BlockOracle {
	return o
}

var _ blocks.BlockOracle = (*Lookup)(nil)
