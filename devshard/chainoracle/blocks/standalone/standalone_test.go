package standalone_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"common/httpguard"
	"github.com/stretchr/testify/require"

	"devshard/chainoracle/blocks/client"
	"devshard/chainoracle/blocks/standalone"
	"devshard/chainoracle/blocks/verifier"
	"devshard/signing"
)

func TestMain(m *testing.M) {
	httpguard.SetAllowPrivate(true)
	os.Exit(m.Run())
}

// ephemeralListener binds :0 and returns a listener + bound addr.
func ephemeralListener(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	return l
}

// genValidators produces n standalone.Validator entries plus the
// matching verifier.Validator pins so consumers can authenticate the
// producer.
func genValidators(t *testing.T, n int) ([]standalone.Validator, []verifier.Validator) {
	t.Helper()
	svs := make([]standalone.Validator, 0, n)
	vvs := make([]verifier.Validator, 0, n)
	for range n {
		s, err := signing.GenerateKey()
		require.NoError(t, err)
		addr, err := blocks.AddressBytes(s.PublicKeyBytes())
		require.NoError(t, err)
		svs = append(svs, standalone.Validator{Signer: s, Address: addr, Power: 1})
		vvs = append(vvs, verifier.Validator{Address: append([]byte(nil), addr...), Power: 1})
	}
	return svs, vvs
}

// newService builds a Service backed by 10 validators (the mock-mainnet
// default) and returns the matching validator set so tests can verify
// the stream end-to-end.
func newService(t *testing.T) (*standalone.Service, string, *verifier.ValidatorSet) {
	t.Helper()

	svs, vvs := genValidators(t, 10)

	lis := ephemeralListener(t)
	// Use a cadence long enough that the ticker never fires during a
	// test; the observer still emits the genesis header on Run start,
	// and tests drive subsequent heights via AdvanceOne.
	svc, err := standalone.New(standalone.Config{
		ChainID:       "gonka-testenv-1",
		Validators:    svs,
		BlockInterval: time.Hour,
		InitialHeight: 1,
		Seed:          42,
		Start:         time.Unix(0, 0).UTC(),
		Listener:      lis,
	})
	require.NoError(t, err)

	vs, err := verifier.NewValidatorSet("gonka-testenv-1", vvs)
	require.NoError(t, err)

	return svc, "http://" + svc.Addr(), vs
}

// runInBackground drives Service.Run and cancels ctx on test cleanup so
// the goroutine always exits before the test returns.
func runInBackground(t *testing.T, svc *standalone.Service) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Go(func() {
		if err := svc.Run(ctx); err != nil {
			t.Errorf("standalone.Run: %v", err)
		}
	})

	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
	return cancel
}

// httpGet is a shorthand that applies a short test timeout per request.
func httpGet(t *testing.T, url string) (*http.Response, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, body
}

// TestNew_RejectsInvalidConfig ensures the constructor catches the
// common misconfigurations before any goroutine starts.
func TestNew_RejectsInvalidConfig(t *testing.T) {
	good, _ := genValidators(t, 3)

	cases := []struct {
		name    string
		mutate  func(*standalone.Config)
		wantErr string
	}{
		{"empty chain id", func(c *standalone.Config) { c.ChainID = "" }, "chain id"},
		{"empty validators", func(c *standalone.Config) { c.Validators = nil }, "at least one validator"},
		{"nil signer", func(c *standalone.Config) {
			c.Validators = []standalone.Validator{{Address: good[0].Address, Power: 1}}
		}, "nil signer"},
		{"bad validator addr", func(c *standalone.Config) {
			c.Validators = []standalone.Validator{
				{Signer: good[0].Signer, Address: []byte{1, 2, 3}, Power: 1},
			}
		}, "20 bytes"},
		{"no listener or addr", func(c *standalone.Config) { c.Addr = ""; c.Listener = nil }, "Listener or Addr"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := standalone.Config{
				ChainID:    "gonka-testenv-1",
				Validators: append([]standalone.Validator(nil), good...),
				Addr:       "127.0.0.1:0",
			}
			tc.mutate(&cfg)
			_, err := standalone.New(cfg)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestService_Healthz verifies the /healthz endpoint is mounted and the
// server responds before the observer has produced any headers.
func TestService_Healthz(t *testing.T) {
	svc, base, _ := newService(t)
	runInBackground(t, svc)

	resp, body := httpGet(t, base+"/healthz")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "ok", strings.TrimSpace(string(body)))
}

// TestService_AtAfterAdvance drives the observer manually (no
// dependency on wall clock) and checks GET /block/:height returns a
// verifiable header.
func TestService_AtAfterAdvance(t *testing.T) {
	svc, base, vs := newService(t)
	runInBackground(t, svc)

	got := waitForHeight(t, base, 1)
	require.Equal(t, "gonka-testenv-1", got.ChainID)
	require.EqualValues(t, 1, got.Height)

	_, err := svc.Observer().AdvanceOne()
	require.NoError(t, err)
	got = waitForHeight(t, base, 2)
	require.EqualValues(t, 2, got.Height)

	v := verifier.New(vs)
	require.NoError(t, v.Verify(got, 0))
	require.GreaterOrEqual(t, len(got.Commit.Signatures), 8,
		"multi-validator mock must retain > 3/4 of signatures")
}

func waitForHeight(t *testing.T, base string, height int64) *blocks.Header {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, body := httpGet(t, fmt.Sprintf("%s/block/%d", base, height))
		if resp.StatusCode == http.StatusOK {
			var h blocks.Header
			require.NoError(t, json.Unmarshal(body, &h))
			return &h
		}
		if time.Now().After(deadline) {
			t.Fatalf("observer did not produce height %d in time (status %d)", height, resp.StatusCode)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestService_LookupAtVerifiedHeaders uses the unary lookup client against
// GET /block/:height after AdvanceOne.
func TestService_LookupAtVerifiedHeaders(t *testing.T) {
	svc, base, vs := newService(t)
	runInBackground(t, svc)
	_ = waitForHeight(t, base, 1)
	for range 3 {
		_, err := svc.Observer().AdvanceOne()
		require.NoError(t, err)
	}
	_ = waitForHeight(t, base, 4)

	cli, err := client.NewLookup(client.HTTPConfig{
		BaseURL:  base,
		Verifier: verifier.New(vs),
	})
	require.NoError(t, err)

	for i := int64(1); i <= 4; i++ {
		h, err := cli.At(context.Background(), i)
		require.NoError(t, err)
		require.Equal(t, i, h.Height)
		require.Equal(t, "gonka-testenv-1", h.ChainID)
		require.GreaterOrEqual(t, len(h.Commit.Signatures), 8)
	}
}

// TestService_HostTrustMode asserts a host-style consumer (nil Verifier)
// receives the full header including every Commit.Signature.
func TestService_HostTrustMode(t *testing.T) {
	svc, base, _ := newService(t)
	runInBackground(t, svc)
	_ = waitForHeight(t, base, 1)
	_, err := svc.Observer().AdvanceOne()
	require.NoError(t, err)
	_ = waitForHeight(t, base, 2)

	cli, err := client.NewLookup(client.HTTPConfig{BaseURL: base})
	require.NoError(t, err)

	for i := int64(1); i <= 2; i++ {
		h, err := cli.At(context.Background(), i)
		require.NoError(t, err)
		require.NotNil(t, h.Commit.Signatures)
		require.GreaterOrEqual(t, len(h.Commit.Signatures), 8,
			"trusted consumer still receives full multi-sig commit")
		for _, sig := range h.Commit.Signatures {
			require.Len(t, sig.Signature, 65)
			require.Len(t, sig.ValidatorAddress, 20)
		}
	}
}

// TestService_RejectsSubsequentReadOfMissingHeight asserts that
// /block/:height returns 404 for never-produced heights, so consumers
// can distinguish "not yet" from transport failures.
func TestService_RejectsUnknownHeight(t *testing.T) {
	svc, base, _ := newService(t)
	runInBackground(t, svc)

	resp, _ := httpGet(t, base+"/block/999")
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestService_RunReturnsOnContextCancel proves Run unblocks when its
// parent context is cancelled, without leaking goroutines.
func TestService_RunReturnsOnContextCancel(t *testing.T) {
	svs, _ := genValidators(t, 4)
	lis := ephemeralListener(t)
	svc, err := standalone.New(standalone.Config{
		ChainID:       "gonka-testenv-1",
		Validators:    svs,
		BlockInterval: 25 * time.Millisecond,
		InitialHeight: 1,
		Seed:          1,
		Listener:      lis,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	// Give the observer a moment to produce at least one header.
	time.Sleep(60 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}
