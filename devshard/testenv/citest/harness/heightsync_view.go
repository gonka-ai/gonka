package harness

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"devshard/heightsync"

	"github.com/stretchr/testify/require"
)

const (
	debugHeightSyncRoute       = "/v1/debug/heightsync"
	heightSyncViewPollInterval = 2 * time.Second
)

// HeightSyncTip is one host's height claim as the gateway's height-sync view reports it.
type HeightSyncTip struct {
	Slot   uint32 `json:"slot"`
	Height uint64 `json:"height"`
	Fresh  bool   `json:"fresh"`
}

// HeightSyncPeerSeen is one observer's count of the subjects it sees.
type HeightSyncPeerSeen struct {
	Observer uint32 `json:"observer"`
	Count    int    `json:"count"`
}

// HeightSyncView is the part of one escrow's GET /v1/debug/heightsync entry the citests read.
type HeightSyncView struct {
	DevshardID          string               `json:"devshard_id"`
	Tips                []HeightSyncTip      `json:"tips"`
	CadenceCounts       map[string]uint64    `json:"cadence_counts"`
	AbandonedTurns      uint64               `json:"abandoned_turns"`
	PeerSeen            []HeightSyncPeerSeen `json:"peer_seen"`
	AnchorsLastSealed   *struct{}            `json:"anchors_last_sealed"`
	BlocksWithoutAnchor uint64               `json:"blocks_without_anchor"`
}

// HeightSpread is the highest height claim minus the lowest, stale claims included.
func (view HeightSyncView) HeightSpread() (uint64, bool) {
	if len(view.Tips) == 0 {
		return 0, false
	}
	lowest, highest := view.Tips[0].Height, view.Tips[0].Height
	for _, tip := range view.Tips[1:] {
		lowest, highest = min(lowest, tip.Height), max(highest, tip.Height)
	}
	return highest - lowest, true
}

// FreshTips and StaleTips count the claims inside and past the originator freshness window.
func (view HeightSyncView) FreshTips() int { return view.countTips(true) }

func (view HeightSyncView) StaleTips() int { return view.countTips(false) }

func (view HeightSyncView) countTips(fresh bool) int {
	count := 0
	for _, tip := range view.Tips {
		if tip.Fresh == fresh {
			count++
		}
	}
	return count
}

// CadenceCount is how many times the cadence recorded event.
func (view HeightSyncView) CadenceCount(event heightsync.CadenceEventKind) uint64 {
	return view.CadenceCounts[string(event)]
}

// GetHeightSyncViews reads every escrow's entry from admin GET /v1/debug/heightsync.
func GetHeightSyncViews(t *testing.T, client *http.Client, gatewayURL, adminAPIKey string) []HeightSyncView {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	var body struct {
		Escrows []HeightSyncView `json:"escrows"`
	}
	require.NoError(t, getGatewayJSONWithKey(client, strings.TrimRight(gatewayURL, "/")+debugHeightSyncRoute, adminAPIKey, &body))
	return body.Escrows
}

// GetHeightSyncView reads escrowID's entry; found is false when the gateway lists no such escrow.
func GetHeightSyncView(t *testing.T, client *http.Client, gatewayURL, adminAPIKey, escrowID string) (HeightSyncView, bool) {
	t.Helper()
	for _, view := range GetHeightSyncViews(t, client, gatewayURL, adminAPIKey) {
		if view.DevshardID == escrowID {
			return view, true
		}
	}
	return HeightSyncView{}, false
}

// WaitHeightSyncView polls escrowID's height-sync view until accept holds and returns that view.
func WaitHeightSyncView(t *testing.T, client *http.Client, gatewayURL, escrowID string, timeout time.Duration, accept func(HeightSyncView) bool) HeightSyncView {
	t.Helper()
	var last HeightSyncView
	listed := false
	ok := AssertEventually(t, timeout, heightSyncViewPollInterval, func() bool {
		last, listed = GetHeightSyncView(t, client, gatewayURL, TestenvAdminAPIKey, escrowID)
		return listed && accept(last)
	})
	require.True(t, ok, "height-sync view of escrow %s not accepted within %s (listed=%t): %+v", escrowID, timeout, listed, last)
	return last
}
