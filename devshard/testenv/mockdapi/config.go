package mockdapi

import (
	"net/http"
	"time"
)

// Config parameterizes mockdapi.New.
//
// The testenv variant does not pin a validator set: the oracle runs in
// host-trust mode (see devshard/blockoracle/client.HTTPConfig.Verifier).
// Hosts have an authenticated relationship with the height-sync
// container; the validator pubkey list lives on devshardctl, which
// mounts its own verifying client against the same endpoint. See
// devshard/docs/testenv.md §3.5 and §5.
type Config struct {
	// HeightSyncURL is the base URL of the height-sync container
	// (e.g. "http://height-sync:9100"). Must be set.
	HeightSyncURL string

	// ChainID is informational today: it is not used to filter headers
	// because the trust-mode client does not verify them. Retained on
	// the Config so the testenv harness can log a sanity check if it
	// ever decides to.
	ChainID string

	// ResubscribeAfter is the SSE reconnect backoff cap passed through
	// to the underlying blockoracle client. Zero = client default (1s).
	ResubscribeAfter time.Duration

	// StaleAfter is how long a cached header remains fresh before
	// Latest() starts marking it stale. Zero = client default (10s),
	// which is appropriate when blocks arrive every few seconds. For
	// slow mock chains (>10s block interval) raise this so callers do
	// not see spurious stale markers on every Latest() read.
	StaleAfter time.Duration

	// HTTPClient lets tests inject a pre-configured client (e.g. one
	// pointed at an httptest server). Nil = use the blockoracle
	// client's default (no client-side timeout, SSE-friendly).
	HTTPClient *http.Client
}
