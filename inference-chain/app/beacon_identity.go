package app

// Block BLS beacon, Step 2 review M2: the share provider only uses shares whose
// slot owner is this node's own validator. The node learns the consensus
// address it signs with from its local CometBFT RPC (/status
// validator_info.address), which also reports the key held by a remote signer
// (tmkms). The lookup runs in the background, never inside ABCI handlers.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"cosmossdk.io/log"
)

const (
	// FlagRPCListenAddr is CometBFT's config.toml rpc.laddr (the server viper
	// merges config.toml and app.toml).
	FlagRPCListenAddr = "rpc.laddr"

	identityRetryMin     = 2 * time.Second
	identityRetryMax     = 30 * time.Second
	identityWarnAfter    = 2 * time.Minute
	identityHTTPTimeout  = 5 * time.Second
	identityMaxBodyBytes = 1 << 20
)

// cometSelfIdentity implements beacon.SelfIdentity by polling the local
// CometBFT /status until it reports a validator address. The consensus key
// can only change with a node restart, so one successful read is final.
type cometSelfIdentity struct {
	statusURL string
	client    *http.Client
	logger    log.Logger

	mu   sync.RWMutex
	addr []byte

	cancel context.CancelFunc
	done   chan struct{}
}

// ConsensusAddress implements beacon.SelfIdentity.
func (c *cometSelfIdentity) ConsensusAddress() ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.addr, len(c.addr) > 0
}

// newCometSelfIdentity builds the identity source for a CometBFT rpc.laddr
// (tcp://host:port or unix:///path). An empty laddr uses the CometBFT default.
func newCometSelfIdentity(laddr string, logger log.Logger) (*cometSelfIdentity, error) {
	if laddr == "" {
		laddr = "tcp://127.0.0.1:26657"
	}
	statusURL, client, err := statusEndpoint(laddr)
	if err != nil {
		return nil, err
	}
	return &cometSelfIdentity{statusURL: statusURL, client: client, logger: logger}, nil
}

// statusEndpoint maps rpc.laddr to a /status URL on loopback.
func statusEndpoint(laddr string) (string, *http.Client, error) {
	u, err := url.Parse(laddr)
	if err != nil {
		return "", nil, fmt.Errorf("rpc.laddr %q: %w", laddr, err)
	}
	switch u.Scheme {
	case "tcp", "http", "":
		host, port, err := net.SplitHostPort(u.Host)
		if err != nil {
			return "", nil, fmt.Errorf("rpc.laddr %q: %w", laddr, err)
		}
		// Listening on all interfaces: talk to it on loopback.
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		return "http://" + net.JoinHostPort(host, port) + "/status", &http.Client{Timeout: identityHTTPTimeout}, nil
	case "unix":
		path := u.Path
		if path == "" {
			path = u.Opaque
		}
		if path == "" {
			return "", nil, fmt.Errorf("rpc.laddr %q: empty socket path", laddr)
		}
		tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		}}
		return "http://unix/status", &http.Client{Timeout: identityHTTPTimeout, Transport: tr}, nil
	default:
		return "", nil, fmt.Errorf("rpc.laddr %q: unsupported scheme %q", laddr, u.Scheme)
	}
}

// start polls until the address is known or ctx ends.
func (c *cometSelfIdentity) start(ctx context.Context) {
	ctx, c.cancel = context.WithCancel(ctx)
	c.done = make(chan struct{})
	go c.run(ctx)
}

func (c *cometSelfIdentity) close() {
	if c.cancel != nil {
		c.cancel()
		<-c.done
	}
}

func (c *cometSelfIdentity) run(ctx context.Context) {
	defer close(c.done)
	defer func() {
		if r := recover(); r != nil {
			c.logger.Error("beacon validator identity lookup panicked", "panic", r)
		}
	}()
	began := time.Now()
	warned := false
	wait := identityRetryMin
	for {
		addr, err := c.fetch(ctx)
		if err == nil {
			c.mu.Lock()
			c.addr = addr
			c.mu.Unlock()
			c.logger.Info("beacon validator identity", "cons_address", strings.ToUpper(hex.EncodeToString(addr)))
			return
		}
		if !warned && time.Since(began) > identityWarnAfter {
			c.logger.Warn("beacon validator identity still unknown; beacon shares stay unused", "url", c.statusURL, "err", err)
			warned = true
		} else {
			c.logger.Debug("beacon validator identity not available yet", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if wait *= 2; wait > identityRetryMax {
			wait = identityRetryMax
		}
	}
}

// statusResponse is the part of CometBFT's JSON-RPC /status reply we read.
type statusResponse struct {
	Result struct {
		ValidatorInfo struct {
			Address string `json:"address"`
		} `json:"validator_info"`
	} `json:"result"`
}

func (c *cometSelfIdentity) fetch(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.statusURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, identityMaxBodyBytes))
	if err != nil {
		return nil, err
	}
	return parseStatusAddress(body)
}

// parseStatusAddress extracts validator_info.address (hex, 20 bytes).
func parseStatusAddress(body []byte) ([]byte, error) {
	var st statusResponse
	if err := json.Unmarshal(body, &st); err != nil {
		return nil, fmt.Errorf("decode /status: %w", err)
	}
	a := st.Result.ValidatorInfo.Address
	if a == "" {
		return nil, errors.New("/status has no validator_info.address")
	}
	addr, err := hex.DecodeString(a)
	if err != nil || len(addr) != 20 {
		return nil, fmt.Errorf("/status validator_info.address %q is not a 20-byte hex address", a)
	}
	return addr, nil
}
