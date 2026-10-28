package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/e2e/testutil"
	"devshard/testenv/mockchain/ledgerface"
)

const (
	departureRounds      = 12
	departuresPerRound   = 20
	stayingPerRound      = 4
	departureStep        = 500 * time.Microsecond
	departureWindowShift = 5 * time.Millisecond
	departureHostStallMS = "300"
	settleBusyWait       = 30 * time.Second
	settleBusyPoll       = 200 * time.Millisecond
)

func mockChainTestenvURL(t *testing.T, env *e2eEnv) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testutil.DefaultRequestTimeout)
	defer cancel()
	host, err := env.mockChain.Host(ctx)
	require.NoError(t, err)
	port, err := env.mockChain.MappedPort(ctx, "9191/tcp")
	require.NoError(t, err)
	return "http://" + host + ":" + port.Port()
}

func setEscrowLookupAbsent(t *testing.T, env *e2eEnv, absent bool) {
	t.Helper()
	answered := testutil.PostJSONRaw(t, &http.Client{Timeout: testutil.DefaultRequestTimeout},
		mockChainTestenvURL(t, env)+"/testenv/escrow-query-fault", map[string]any{"absent": absent}, "")
	require.Equal(t, http.StatusOK, answered.StatusCode, "escrow-query-fault: %s", answered.Body)
}

func chainEscrowSettled(t *testing.T, client *http.Client, env *e2eEnv, escrowID string) bool {
	t.Helper()
	response := testutil.GetRaw(t, client, mockChainTestenvURL(t, env)+"/testenv/escrows", "")
	require.Equal(t, http.StatusOK, response.StatusCode, "testenv/escrows: %s", response.Body)
	var listing struct {
		Escrows []ledgerface.EscrowView `json:"escrows"`
	}
	require.NoError(t, json.Unmarshal([]byte(response.Body), &listing))
	for _, escrow := range listing.Escrows {
		if strconv.FormatUint(escrow.ID, 10) == escrowID {
			return escrow.Settled
		}
	}
	t.Fatalf("the mock chain holds no escrow %s: %s", escrowID, response.Body)
	return false
}

func settleRetryingBusy(t *testing.T, client *http.Client, url string) testutil.RawResponse {
	t.Helper()
	deadline := time.Now().Add(settleBusyWait)
	for {
		response := gatewayPost(t, client, url)
		if response.StatusCode != http.StatusConflict || !time.Now().Before(deadline) {
			return response
		}
		time.Sleep(settleBusyPoll)
	}
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Make the mock chain answer every escrow lookup with not found, as a node behind the create height does.
//  3. Settle the escrow through the admin route, retrying while it answers 409 busy (the startup warm-up holds the escrow), and assert an error other than 410 Gone or 409: the escrow's epoch is inside its settlement window, so the absent answer is lag, not a prune.
//  4. Assert the gateway still holds the escrow parked for settlement, not gone from chain, and the chain has not settled it.
//  5. Clear the lag, settle again while it answers busy and assert 200 and the escrow settled on chain.
func TestE2E_GatewaySettlesALiveEscrowThroughALaggingLookup(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})
	admin := env.clientURL + "/v1/admin/devshards/" + defaultEscrowID

	setEscrowLookupAbsent(t, env, true)
	lagging := settleRetryingBusy(t, client, admin+"/settle")
	if lagging.StatusCode == http.StatusGone || lagging.StatusCode == http.StatusConflict || lagging.StatusCode < http.StatusBadRequest {
		t.Fatalf("settling a live escrow while the chain lookup lags = %d %s, want an error that keeps it for a retry", lagging.StatusCode, lagging.Body)
	}
	held := testutil.ListedDevshard(t, client, env.clientURL, defaultEscrowID, testutil.AdminAPIKey)
	if held.Active || !held.SettlementPending || held.GoneFromChain {
		t.Fatalf("after a lagging settle the escrow row is %+v, want it parked for settlement and not gone from chain", held)
	}
	if chainEscrowSettled(t, client, env, defaultEscrowID) {
		t.Fatalf("the chain holds escrow %s as settled after a settle answered %d", defaultEscrowID, lagging.StatusCode)
	}

	setEscrowLookupAbsent(t, env, false)
	settled := settleRetryingBusy(t, client, admin+"/settle")
	if settled.StatusCode != http.StatusOK {
		t.Fatalf("settling the escrow once the lookup caught up = %d %s, want 200", settled.StatusCode, settled.Body)
	}
	if !chainEscrowSettled(t, client, env, defaultEscrowID) {
		t.Errorf("the gateway answered 200 but the chain does not hold escrow %s as settled", defaultEscrowID)
	}
}

func sendAndHangUp(client *http.Client, clientURL, content string, after time.Duration) (served bool, err error) {
	ctx, hangUp := context.WithTimeout(context.Background(), after)
	defer hangUp()
	body, err := json.Marshal(testutil.ChatCompletionBody(content, false))
	if err != nil {
		return false, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, clientURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+testutil.AdminAPIKey)
	response, err := client.Do(request)
	if err != nil {
		return false, nil
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK, nil
}

// Test flow:
//  1. Start the three-host gateway environment with every host stalling its answer, so requests stay in flight.
//  2. Fire rounds of concurrent completions whose clients hang up at half-millisecond steps across a ten-millisecond window that slides by five milliseconds a round over the first 25 milliseconds, where the round's nonces are committed.
//  3. Assert some clients left and some were served, so departures landed on both sides of the nonce commit.
//  4. Assert no nonce was burned as request_abandoned_before_dispatch: every committed nonce was sent to its host.
//  5. Assert the ledger accounts every assigned nonce exactly once and raised no findings.
func TestE2E_GatewaySendsACommittedNonceAfterTheClientLeaves(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{
		hostEnvOverrides: testutil.HostsStalling(e2eHostCount, departureHostStallMS),
	})

	var mu sync.Mutex
	served, departed := 0, 0
	for round := range departureRounds {
		var pending sync.WaitGroup
		for index := range departuresPerRound + stayingPerRound {
			hangUpAfter := time.Duration(round%4)*departureWindowShift + time.Duration(round/4%2)*departureStep/2 +
				time.Duration(index+1)*departureStep
			if index >= departuresPerRound {
				hangUpAfter = testutil.DefaultRequestTimeout
			}
			pending.Add(1)
			go func() {
				defer pending.Done()
				ok, err := sendAndHangUp(client, env.clientURL, fmt.Sprintf("departure %d-%d", round, index), hangUpAfter)
				if err != nil {
					t.Errorf("request %d-%d: %v", round, index, err)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				if ok {
					served++
				} else {
					departed++
				}
			}()
		}
		pending.Wait()
	}
	if departed == 0 || served == 0 {
		t.Fatalf("%d clients hung up and %d were served, want both: the departures did not straddle the answer", departed, served)
	}
	t.Logf("%d clients hung up, %d were served", departed, served)

	if abandoned := gatewayGhostBurns(t, client, env.statsURL, "request_abandoned_before_dispatch"); abandoned > 0 {
		t.Errorf("%d committed nonces were burned as abandoned instead of sent: ghost reasons %v",
			abandoned, gatewayGhostReasons(t, client, env.statsURL))
	}
	testutil.RequireNonceAccountingBalanced(t, gatewayLedger(t, client, env.statsURL))
	if codes := gatewayFindings(t, client, env.statsURL); len(codes) > 0 {
		t.Errorf("clients hanging up raised findings %v, want none", codes)
	}
}
