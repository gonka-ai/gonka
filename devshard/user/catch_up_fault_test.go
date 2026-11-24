package user

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"devshard/storage"
	"devshard/transport"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Inside a synctest bubble, send to a far-behind host whose first three catch-up chunk tries go unanswered (peer session not ready), the fourth landing.
//  2. Assert the send's fault hook ran once, at the first failed try, well before the catch-up finished.
//  3. Start another send after the chunk landed and assert its hook does not run.
func TestCatchUpForSend_ReportsTheFirstUnansweredChunkTry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session := setupStoredSession(t, storage.NewMemory())
		const n = catchUpChunkSize + 20
		composeEmptyDiffs(t, session, n)
		unreachable := fmt.Errorf("%w: attach timed out", transport.ErrPeerNotReady)
		wrapFaulty(session, 1, unreachable, unreachable, unreachable)

		start := time.Now()
		var faults []time.Duration
		_, err := session.catchUpForSendWith(context.Background(), 1, n, func() {
			faults = append(faults, time.Since(start))
		})
		finished := time.Since(start)

		require.NoError(t, err)
		require.Equal(t, []time.Duration{0}, faults)
		require.Greater(t, finished, time.Second)

		composeEmptyDiffs(t, session, catchUpChunkSize+20)
		_, err = session.catchUpForSendWith(context.Background(), 1, 2*n, func() {
			t.Error("a host whose last chunk landed was reported unreachable")
		})
		require.NoError(t, err)
		session.stopSendCatchUps()
	})
}

// Test flow:
//  1. Inside a synctest bubble, send to a far-behind host whose chunk tries are answered with storage_busy and a 500 before one lands.
//  2. Assert the send's fault hook never ran: a host that answers keeps the full catch-up minute.
func TestCatchUpForSend_AnAnsweringHostIsNotReportedUnreachable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session := setupStoredSession(t, storage.NewMemory())
		const n = catchUpChunkSize + 20
		composeEmptyDiffs(t, session, n)
		busy := &transport.UpstreamStatusError{StatusCode: http.StatusServiceUnavailable, DevshardError: transport.DevshardErrorStorageBusy}
		wrapFaulty(session, 1, busy, status500())

		_, err := session.catchUpForSendWith(context.Background(), 1, n, func() {
			t.Error("a host that answered its catch-up was reported unreachable")
		})

		require.NoError(t, err)
		session.stopSendCatchUps()
	})
}
