package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
)

func TestOfferRescan_QuietEscrowOffers(t *testing.T) {
	escrow := "escrow-rescan-quiet"
	capSched := &captureOfferScheduler{}
	h, hosts, user := newRescanHost(t, escrow, capSched)
	// Apply finish before Start so there is no traffic Offer.
	finishInference(t, h, hosts, user, escrow)
	require.False(t, h.IsValidating(1))

	h.Start()
	t.Cleanup(h.Close)

	rescan := &OfferRescan{
		manager:  &stubSessionManager{ids: []string{escrow}, host: h},
		interval: DefaultOfferRescanInterval,
	}
	rescan.runOnce()

	jobs := capSched.snapshot()
	require.Len(t, jobs, 1)
	assert.Equal(t, uint64(1), jobs[0].InferenceID)
	assert.False(t, jobs[0].LeaseHeld)
	assert.True(t, h.IsValidating(1))
}

func TestOfferRescan_RespectsValidating(t *testing.T) {
	escrow := "escrow-rescan-validating"
	capSched := &captureOfferScheduler{}
	h, hosts, user := newRescanHost(t, escrow, capSched)
	finishInference(t, h, hosts, user, escrow)
	h.Start()
	t.Cleanup(h.Close)

	rescan := &OfferRescan{
		manager:  &stubSessionManager{ids: []string{escrow}, host: h},
		interval: DefaultOfferRescanInterval,
	}
	rescan.runOnce()
	require.True(t, h.IsValidating(1))
	before := len(capSched.snapshot())

	rescan.runOnce()
	rescan.runOnce()
	assert.Equal(t, before, len(capSched.snapshot()), "must not double-offer while validating")
}

func TestOfferRescan_SkipsClosedHost(t *testing.T) {
	escrow := "escrow-rescan-closed"
	capSched := &captureOfferScheduler{}
	h, hosts, user := newRescanHost(t, escrow, capSched)
	finishInference(t, h, hosts, user, escrow)
	h.Start()
	h.Close()

	rescan := &OfferRescan{
		manager:  &stubSessionManager{ids: []string{escrow}, host: h},
		interval: DefaultOfferRescanInterval,
	}
	rescan.runOnce()
	assert.Empty(t, capSched.snapshot())
}

func newRescanHost(t *testing.T, escrow string, sched host.ValidationScheduler) (*host.Host, []*signing.Secp256k1Signer, *signing.Secp256k1Signer) {
	t.Helper()
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := types.SessionConfig{
		RefusalTimeout: 60, ExecutionTimeout: 1200, TokenPrice: 1, VoteThreshold: 1, ValidationRate: 10000,
	}
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine(escrow, config, group, 100000, user.Address(), verifier,
		testutil.MustMemoryStore(t, escrow, user.Address(), config, group, 100000))
	require.NoError(t, err)
	h, err := host.NewHost(sm, hosts[0], stub.NewInferenceEngine(), escrow, group, nil,
		host.WithGrace(10),
		host.WithValidator(&trackingEngine{valid: true}),
		host.WithValidationScheduler(sched),
	)
	require.NoError(t, err)
	return h, hosts, user
}

func TestClampOfferRescanInterval(t *testing.T) {
	assert.Equal(t, minOfferRescanInterval, clampOfferRescanInterval(time.Second))
	assert.Equal(t, maxOfferRescanInterval, clampOfferRescanInterval(time.Hour))
	assert.Equal(t, 20*time.Second, clampOfferRescanInterval(20*time.Second))
}
