// Package standalone wires observer.NewMock + server.Mount into an HTTP
// listener so the height-sync container can be a trivial shim over the
// reusable blockoracle module.
//
// It is kept as a library (not a main package) so:
//
//   - cmd/heightsyncd stays a small main that only loads config and
//     calls Run.
//   - Unit tests can construct the Service with a pre-built
//     net.Listener (e.g. :0 ephemeral) without starting a full process.
//   - A future prod-side standalone oracle can reuse the exact wiring.
//
// The package intentionally depends only on devshard/blockoracle/* and
// standard library / echo. It does not import devshard/testenv so
// production (decentralized-api) could vendor it without pulling in
// testenv code; see devshard/docs/testenv.md §3.6.
package standalone

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"devshard/blockoracle/observer"
	"devshard/blockoracle/server"
	"devshard/signing"
)

// Validator pins one participant of the producer-side validator set.
// It mirrors observer.MockValidator so the heightsyncd binary does not
// have to depend on the observer package directly.
type Validator struct {
	Signer  *signing.Secp256k1Signer
	Address []byte // 20-byte address derived from Signer.PublicKeyBytes()
	Power   int64  // voting power; must be > 0
}

// Config pins everything Run needs. A zero value is not usable;
// the constructor validates required fields.
type Config struct {
	// ChainID must match the value pinned on every consumer.
	ChainID string
	// Validators is the full pinned validator set. Every fabricated
	// header is signed by all of them except a deterministic subset
	// chosen per-height; the remaining power is always strictly > 3/4
	// of the total (stricter than the verifier's > 2/3 rule).
	Validators []Validator
	// BlockInterval controls the mock observer's cadence; ≤0 falls back
	// to 1s (observer default).
	BlockInterval time.Duration
	// InitialHeight of the first fabricated block; ≤0 falls back to 1.
	InitialHeight int64
	// Seed makes the hash derivation deterministic; same seed ⇒ same
	// BlockHash/AppHash/… across restarts.
	Seed int64
	// Start timestamp of the first block. Zero ⇒ time.Unix(0, 0).
	Start time.Time

	// Listener is the network listener to serve HTTP on. When nil, Run
	// falls back to Addr. Tests pass a :0 listener; the binary passes
	// ":9100".
	Listener net.Listener
	// Addr is the fallback listen address (e.g. ":9100"). Ignored when
	// Listener is non-nil.
	Addr string

	// ShutdownTimeout bounds the HTTP graceful shutdown. 0 ⇒ 5s.
	ShutdownTimeout time.Duration
}

// Service owns the observer + HTTP listener pair. It exists so tests can
// read .Addr() back after ephemeral binding, and drive the observer with
// AdvanceOne between requests.
type Service struct {
	observer *observer.Mock
	srv      *http.Server
	listener net.Listener
	cfg      Config
}

// New constructs the observer and HTTP server without starting them.
// Returns an error if the config is incomplete.
func New(cfg Config) (*Service, error) {
	if cfg.ChainID == "" {
		return nil, errors.New("standalone: empty chain id")
	}
	if len(cfg.Validators) == 0 {
		return nil, errors.New("standalone: at least one validator is required")
	}
	obsValidators := make([]observer.MockValidator, len(cfg.Validators))
	for i, v := range cfg.Validators {
		if v.Signer == nil {
			return nil, fmt.Errorf("standalone: validators[%d] has nil signer", i)
		}
		if len(v.Address) != 20 {
			return nil, fmt.Errorf("standalone: validators[%d] address must be 20 bytes, got %d",
				i, len(v.Address))
		}
		if v.Power <= 0 {
			return nil, fmt.Errorf("standalone: validators[%d] power must be > 0", i)
		}
		obsValidators[i] = observer.MockValidator{
			Signer:  v.Signer,
			Address: append([]byte(nil), v.Address...),
			Power:   v.Power,
		}
	}

	lis := cfg.Listener
	if lis == nil {
		addr := cfg.Addr
		if addr == "" {
			return nil, errors.New("standalone: either Listener or Addr must be set")
		}
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("standalone: listen %s: %w", addr, err)
		}
		lis = l
	}

	mock, err := observer.NewMock(observer.MockConfig{
		ChainID:       cfg.ChainID,
		Validators:    obsValidators,
		BlockInterval: cfg.BlockInterval,
		Seed:          cfg.Seed,
		Start:         cfg.Start,
		InitialHeight: cfg.InitialHeight,
	})
	if err != nil {
		_ = lis.Close()
		return nil, fmt.Errorf("standalone: observer: %w", err)
	}

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	server.Mount(e.Group(""), mock)

	s := &Service{
		observer: mock,
		srv: &http.Server{
			Handler:           e,
			ReadHeaderTimeout: 5 * time.Second,
		},
		listener: lis,
		cfg:      cfg,
	}
	return s, nil
}

// Addr returns the bound listener address, useful in tests that use :0.
func (s *Service) Addr() string {
	if s == nil || s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Observer exposes the underlying mock so tests can AdvanceOne between
// assertions. Not safe for use from production code; production will mount
// a real observer.
func (s *Service) Observer() *observer.Mock { return s.observer }

// Run starts both the observer loop and the HTTP listener and blocks
// until ctx is cancelled. It shuts both down gracefully on exit.
//
// Returns nil on clean ctx.Cancel; returns an underlying error only when
// the HTTP listener or observer fail for reasons other than shutdown.
func (s *Service) Run(ctx context.Context) error {
	obsErr := make(chan error, 1)
	go func() {
		obsErr <- s.observer.Run(ctx)
	}()

	httpErr := make(chan error, 1)
	go func() {
		err := s.srv.Serve(s.listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		httpErr <- err
	}()

	// Wait for whichever goroutine exits first (typically ctx → obsErr).
	select {
	case <-ctx.Done():
	case err := <-obsErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			// Best-effort: stop the HTTP listener before returning.
			s.shutdownHTTP()
			return fmt.Errorf("standalone: observer: %w", err)
		}
	case err := <-httpErr:
		if err != nil {
			return fmt.Errorf("standalone: http: %w", err)
		}
	}

	// Graceful HTTP shutdown.
	s.shutdownHTTP()

	// Drain any pending goroutine errors so they do not leak.
	<-obsErr
	<-httpErr
	return nil
}

func (s *Service) shutdownHTTP() {
	timeout := s.cfg.ShutdownTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_ = s.srv.Shutdown(shutdownCtx)
}

// Run is a convenience wrapper: New + Run. Binaries call this; tests
// typically call New and drive the Service directly.
func Run(ctx context.Context, cfg Config) error {
	s, err := New(cfg)
	if err != nil {
		return err
	}
	return s.Run(ctx)
}
