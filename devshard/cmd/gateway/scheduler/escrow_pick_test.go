package scheduler

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/types"
)

const modelA = "model-a"

type fakeSession struct {
	balance      uint64
	tokenPrice   uint64
	feePerNonce  uint64
	latestNonce  uint64
	slots        []string
	participants []string
	calls        []string
}

func (f *fakeSession) Advance(func(HostBinding) NonceIntent) (Prepared, error) {
	f.calls = append(f.calls, "Advance")
	return nil, nil
}

func (f *fakeSession) ParticipantKeys() []string {
	f.calls = append(f.calls, "ParticipantKeys")
	return f.participants
}

func (f *fakeSession) ChatBudgetSpent(string) bool { return false }

func (f *fakeSession) SlotParticipants() []string {
	f.calls = append(f.calls, "SlotParticipants")
	return f.slots
}

func (f *fakeSession) GroupSize() int {
	f.calls = append(f.calls, "GroupSize")
	return len(f.slots)
}

func (f *fakeSession) LatestNonce() uint64 {
	f.calls = append(f.calls, "LatestNonce")
	return f.latestNonce
}

type fakeEscrows struct {
	mu      sync.Mutex
	byModel map[string][]Escrow
	retired map[string]Escrow
	queries []string
}

func (f *fakeEscrows) Candidates(model string) []Escrow {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, model)
	return f.byModel[model]
}

func (f *fakeEscrows) RetiredAtNonceCap(escrowID string) (Escrow, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	retired, ok := f.retired[escrowID]
	return retired, ok
}

type fakeWeights struct {
	mu       sync.Mutex
	byEscrow map[string]float64
	lookups  []string
}

func (f *fakeWeights) EscrowWeight(escrowID, model string) float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups = append(f.lookups, escrowID+"/"+model)
	return f.byEscrow[escrowID]
}

type candidate struct {
	id          string
	activeUsers int
	weight      float64
	latestNonce uint64
	groupSize   int
	slots       []string
	balance     uint64
	tokenPrice  uint64
	isReserve   bool
}

// slotsOf gives one escrow its own hosts, so two candidates in one test never share a block.
func slotsOf(escrowID string, groupSize int) []string {
	slots := make([]string, 0, groupSize)
	for slot := range groupSize {
		slots = append(slots, fmt.Sprintf("%s-host-%d", escrowID, slot))
	}
	return slots
}

func newScheduler(candidates ...candidate) (*Scheduler, *fakeEscrows, *fakeWeights) {
	escrows := &fakeEscrows{byModel: map[string][]Escrow{}}
	weights := &fakeWeights{byEscrow: map[string]float64{}}
	for _, entry := range candidates {
		groupSize := entry.groupSize
		if groupSize == 0 {
			groupSize = 4
		}
		slots := entry.slots
		if len(slots) == 0 {
			slots = slotsOf(entry.id, groupSize)
		}
		escrows.byModel[modelA] = append(escrows.byModel[modelA], Escrow{
			ID:          entry.id,
			Model:       modelA,
			Session:     &fakeSession{latestNonce: entry.latestNonce, slots: slots, balance: entry.balance, tokenPrice: entry.tokenPrice},
			ActiveUsers: entry.activeUsers,
			IsReserve:   entry.isReserve,
		})
		weights.byEscrow[entry.id] = entry.weight
	}
	return &Scheduler{
		escrows:      escrows,
		capacity:     weights,
		limiter:      newFakeLimiter(),
		perf:         &fakePerf{ejected: map[string]bool{}},
		dispatchers:  map[string]*dispatcher{},
		blockedHosts: map[string]map[string]bool{},
	}, escrows, weights
}

// pickWith stands in for the part of Pick that precedes the dispatcher: one waiter built from the profile, nothing avoided.
func pickWith(scheduler *Scheduler, profile RequestProfile, snapshot chain.PhaseSnapshot) (Escrow, error) {
	return scheduler.pickEscrow(profile, snapshot, newWaiter(profile, time.Time{}), nil)
}

// Test flow:
//  1. Build two candidates where the pinned one scores worst by every measure, and pin the request to it.
//  2. Pick an escrow.
//  3. Assert the pinned escrow is returned directly, with no weight lookups at all.
//  4. Pin a request to an escrow id that does not exist and assert the pick fails with `ErrEscrowGone`.
func TestPickEscrowPinned(t *testing.T) {
	t.Parallel()

	t.Run("returns the pinned escrow directly", func(t *testing.T) {
		t.Parallel()
		scheduler, _, weights := newScheduler(
			candidate{id: "escrow-1", activeUsers: 0, weight: 10},
			candidate{id: "escrow-2", activeUsers: 99, weight: 1, latestNonce: 19_000},
		)

		picked, err := pickWith(scheduler, RequestProfile{Model: modelA, Escrow: "escrow-2"}, chain.PhaseSnapshot{})
		if err != nil {
			t.Fatalf("pickEscrow: %v", err)
		}
		if picked.ID != "escrow-2" {
			t.Fatalf("picked %q, want escrow-2", picked.ID)
		}
		if len(weights.lookups) != 0 {
			t.Fatalf("pinned pick weighed escrows: %v", weights.lookups)
		}
	})

	t.Run("errors when the pinned escrow is gone", func(t *testing.T) {
		t.Parallel()
		scheduler, _, _ := newScheduler(candidate{id: "escrow-1", weight: 10})

		_, err := pickWith(scheduler, RequestProfile{Model: modelA, Escrow: "escrow-gone"}, chain.PhaseSnapshot{})
		if !errors.Is(err, ErrEscrowGone) {
			t.Fatalf("err = %v, want ErrEscrowGone", err)
		}
	})
}

// Test flow:
//  1. For each table case of two candidates' active-user counts and weights, pick an escrow.
//  2. Assert the winner matches the case: equal weight sorts by in-flight requests, equal in-flight sorts by weight, and the ratio of the two beats either raw number, with a zero or negative weight never winning.
func TestPickEscrowLowestUtilisationWins(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		candidates []candidate
		want       string
	}{
		{
			name: "equal weight sorts by in-flight requests",
			candidates: []candidate{
				{id: "escrow-1", activeUsers: 8, weight: 10},
				{id: "escrow-2", activeUsers: 2, weight: 10},
			},
			want: "escrow-2",
		},
		{
			name: "equal in-flight sorts by weight",
			candidates: []candidate{
				{id: "escrow-1", activeUsers: 4, weight: 5},
				{id: "escrow-2", activeUsers: 4, weight: 20},
			},
			want: "escrow-2",
		},
		{
			name: "ratio beats raw weight",
			candidates: []candidate{
				{id: "escrow-1", activeUsers: 90, weight: 100},
				{id: "escrow-2", activeUsers: 1, weight: 10},
			},
			want: "escrow-2",
		},
		{
			name: "ratio beats raw in-flight count",
			candidates: []candidate{
				{id: "escrow-1", activeUsers: 1, weight: 1},
				{id: "escrow-2", activeUsers: 10, weight: 100},
			},
			want: "escrow-2",
		},
		{
			name: "an unusable escrow never wins over a loaded usable one",
			candidates: []candidate{
				{id: "escrow-1", activeUsers: 0, weight: 0},
				{id: "escrow-2", activeUsers: 500, weight: 1},
			},
			want: "escrow-2",
		},
		{
			name: "a negative weight never wins",
			candidates: []candidate{
				{id: "escrow-1", activeUsers: 0, weight: -5},
				{id: "escrow-2", activeUsers: 500, weight: 1},
			},
			want: "escrow-2",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			scheduler, _, _ := newScheduler(testCase.candidates...)

			picked, err := pickWith(scheduler, RequestProfile{Model: modelA}, chain.PhaseSnapshot{})
			if err != nil {
				t.Fatalf("pickEscrow: %v", err)
			}
			if picked.ID != testCase.want {
				t.Fatalf("picked %q, want %q", picked.ID, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Build two candidates tied on both weight and active users.
//  2. Pick an escrow three times in a row for the same profile.
//  3. Assert the first two picks differ.
//  4. Assert the third pick wraps back to the first escrow.
func TestPickEscrowTieBreakAdvancesSharedCounter(t *testing.T) {
	t.Parallel()
	scheduler, _, _ := newScheduler(
		candidate{id: "escrow-1", activeUsers: 3, weight: 10},
		candidate{id: "escrow-2", activeUsers: 3, weight: 10},
	)
	profile := RequestProfile{Model: modelA}

	first, err := pickWith(scheduler, profile, chain.PhaseSnapshot{})
	if err != nil {
		t.Fatalf("pickEscrow: %v", err)
	}
	second, err := pickWith(scheduler, profile, chain.PhaseSnapshot{})
	if err != nil {
		t.Fatalf("pickEscrow: %v", err)
	}
	third, err := pickWith(scheduler, profile, chain.PhaseSnapshot{})
	if err != nil {
		t.Fatalf("pickEscrow: %v", err)
	}

	if first.ID == second.ID {
		t.Fatalf("tie-break returned %q twice in a row", first.ID)
	}
	if third.ID != first.ID {
		t.Fatalf("third pick %q, want the counter to wrap back to %q", third.ID, first.ID)
	}
}

// Test flow:
//  1. Build two candidates with a clear winner by active-user count.
//  2. Pick an escrow three times in a row for the same profile.
//  3. Assert every pick returns the same winning escrow.
func TestPickEscrowTieBreakDoesNotAdvanceWithoutATie(t *testing.T) {
	t.Parallel()
	scheduler, _, _ := newScheduler(
		candidate{id: "escrow-1", activeUsers: 1, weight: 10},
		candidate{id: "escrow-2", activeUsers: 9, weight: 10},
	)
	profile := RequestProfile{Model: modelA}

	for attempt := range 3 {
		picked, err := pickWith(scheduler, profile, chain.PhaseSnapshot{})
		if err != nil {
			t.Fatalf("pickEscrow: %v", err)
		}
		if picked.ID != "escrow-1" {
			t.Fatalf("attempt %d picked %q, want escrow-1", attempt, picked.ID)
		}
	}
}

// Test flow:
//  1. For each table case of a governance max-nonce cap and one or two candidates' latest nonces and group sizes, pick an escrow.
//  2. Assert the winner or error matches the case: a capped candidate is dropped in favor of a fresh one, an escrow one nonce below its cutoff is served, one at or past the cutoff is refused with `ErrNoEscrowCapacity`, and the cutoff scales correctly across small, large and oversized caps without wrapping.
func TestPickEscrowNonceCap(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		maxNonce   uint64
		candidates []candidate
		want       string
		wantErr    error
	}{
		{
			name:     "governance cap drops the capped escrow",
			maxNonce: 1_000,
			candidates: []candidate{
				{id: "escrow-1", activeUsers: 0, weight: 100, latestNonce: 995, groupSize: 4},
				{id: "escrow-2", activeUsers: 50, weight: 1, latestNonce: 10, groupSize: 4},
			},
			want: "escrow-2",
		},
		{
			name:     "governance cap keeps an escrow one nonce below the cutoff",
			maxNonce: 1_000,
			candidates: []candidate{
				{id: "escrow-1", activeUsers: 0, weight: 100, latestNonce: 794, groupSize: 4},
			},
			want: "escrow-1",
		},
		{
			name:     "governance cap drops an escrow exactly at the cutoff",
			maxNonce: 1_000,
			candidates: []candidate{
				{id: "escrow-1", activeUsers: 0, weight: 100, latestNonce: 795, groupSize: 4},
			},
			wantErr: ErrNoEscrowCapacity,
		},
		{
			name:     "a large cap keeps an escrow one nonce below the cutoff",
			maxNonce: 1_000_000,
			candidates: []candidate{
				{id: "escrow-1", activeUsers: 0, weight: 100, latestNonce: 999_795, groupSize: 3},
			},
			want: "escrow-1",
		},
		{
			name:     "a large cap drops an escrow at the cutoff",
			maxNonce: 1_000_000,
			candidates: []candidate{
				{id: "escrow-1", activeUsers: 0, weight: 100, latestNonce: 999_796, groupSize: 3},
			},
			wantErr: ErrNoEscrowCapacity,
		},
		{
			name:     "a small cap keeps an escrow one nonce below the half-cap cutoff",
			maxNonce: 150,
			candidates: []candidate{
				{id: "escrow-1", activeUsers: 0, weight: 100, latestNonce: 72, groupSize: 4},
			},
			want: "escrow-1",
		},
		{
			name:     "a small cap drops an escrow at the half-cap cutoff",
			maxNonce: 150,
			candidates: []candidate{
				{id: "escrow-1", activeUsers: 0, weight: 100, latestNonce: 73, groupSize: 4},
			},
			wantErr: ErrNoEscrowCapacity,
		},
		{
			name:     "the cutoff does not collapse once the hosts' cap passes the margin",
			maxNonce: 206,
			candidates: []candidate{
				{id: "escrow-1", activeUsers: 0, weight: 100, latestNonce: 100, groupSize: 4},
			},
			want: "escrow-1",
		},
		{
			name:     "unfetched cap keeps an escrow just below the fallback ceiling",
			maxNonce: 0,
			candidates: []candidate{
				{id: "escrow-1", activeUsers: 0, weight: 100, latestNonce: 19_799},
			},
			want: "escrow-1",
		},
		{
			name:     "unfetched cap drops an escrow at the fallback ceiling",
			maxNonce: 0,
			candidates: []candidate{
				{id: "escrow-1", activeUsers: 0, weight: 100, latestNonce: 19_800},
			},
			wantErr: ErrNoEscrowCapacity,
		},
		{
			name:     "unfetched cap drops an escrow above the fallback ceiling",
			maxNonce: 0,
			candidates: []candidate{
				{id: "escrow-1", activeUsers: 0, weight: 100, latestNonce: 19_801},
				{id: "escrow-2", activeUsers: 50, weight: 1, latestNonce: 5},
			},
			want: "escrow-2",
		},
		{
			name:     "an oversized cap clamps instead of wrapping",
			maxNonce: uint64(math.MaxUint32) + 1,
			candidates: []candidate{
				{id: "escrow-1", activeUsers: 0, weight: 100, latestNonce: math.MaxUint32, groupSize: 4},
			},
			wantErr: ErrNoEscrowCapacity,
		},
		{
			name:     "an oversized cap still admits an escrow below the clamped cutoff",
			maxNonce: uint64(math.MaxUint32) + 1,
			candidates: []candidate{
				{id: "escrow-1", activeUsers: 0, weight: 100, latestNonce: math.MaxUint32 - 300, groupSize: 4},
			},
			want: "escrow-1",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			scheduler, _, _ := newScheduler(testCase.candidates...)

			picked, err := pickWith(scheduler, RequestProfile{Model: modelA}, chain.PhaseSnapshot{MaxNonce: testCase.maxNonce})
			if testCase.wantErr != nil {
				if !errors.Is(err, testCase.wantErr) {
					t.Fatalf("err = %v, want %v", err, testCase.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("pickEscrow: %v", err)
			}
			if picked.ID != testCase.want {
				t.Fatalf("picked %q, want %q", picked.ID, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Pick an escrow with every candidate at zero or negative weight, and assert the error is `ErrNoEscrowCapacity` naming neither escrow id nor the model.
//  2. Pick an escrow with an empty candidate set and assert the same error.
//  3. Pick an escrow for a model with no candidates and assert the same error.
func TestPickEscrowNoCapacity(t *testing.T) {
	t.Parallel()

	t.Run("every candidate at non-positive weight", func(t *testing.T) {
		t.Parallel()
		scheduler, _, _ := newScheduler(
			candidate{id: "escrow-1", activeUsers: 0, weight: 0},
			candidate{id: "escrow-2", activeUsers: 0, weight: -1},
		)

		_, err := pickWith(scheduler, RequestProfile{Model: modelA}, chain.PhaseSnapshot{})
		if !errors.Is(err, ErrNoEscrowCapacity) {
			t.Fatalf("err = %v, want ErrNoEscrowCapacity", err)
		}
		for _, secret := range []string{"escrow-1", "escrow-2", modelA} {
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error %q names %q", err, secret)
			}
		}
	})

	t.Run("empty candidate set", func(t *testing.T) {
		t.Parallel()
		scheduler, _, _ := newScheduler()

		_, err := pickWith(scheduler, RequestProfile{Model: modelA}, chain.PhaseSnapshot{})
		if !errors.Is(err, ErrNoEscrowCapacity) {
			t.Fatalf("err = %v, want ErrNoEscrowCapacity", err)
		}
	})

	t.Run("no candidate for the requested model", func(t *testing.T) {
		t.Parallel()
		scheduler, _, _ := newScheduler(candidate{id: "escrow-1", weight: 10})

		_, err := pickWith(scheduler, RequestProfile{Model: "model-b"}, chain.PhaseSnapshot{})
		if !errors.Is(err, ErrNoEscrowCapacity) {
			t.Fatalf("err = %v, want ErrNoEscrowCapacity", err)
		}
	})
}

// Test flow:
//  1. Build two candidates, one of them past the fallback nonce ceiling.
//  2. Pick an escrow.
//  3. Assert candidate enumeration queried the model exactly once.
//  4. Assert only the uncapped escrow was weighed, since the capped one is dropped without a rotation or replacement side effect reaching its session.
//  5. Assert every session call across all candidates was a plain read: `LatestNonce`, `GroupSize` or `SlotParticipants`.
func TestPickEscrowTouchesOnlyEnumerationAndWeights(t *testing.T) {
	t.Parallel()
	scheduler, escrows, weights := newScheduler(
		candidate{id: "escrow-1", activeUsers: 1, weight: 10},
		candidate{id: "escrow-2", activeUsers: 9, weight: 10, latestNonce: 19_900},
	)

	if _, err := pickWith(scheduler, RequestProfile{Model: modelA}, chain.PhaseSnapshot{}); err != nil {
		t.Fatalf("pickEscrow: %v", err)
	}

	if len(escrows.queries) != 1 || escrows.queries[0] != modelA {
		t.Fatalf("candidate enumeration = %v, want one query for %q", escrows.queries, modelA)
	}
	if len(weights.lookups) != 1 || weights.lookups[0] != "escrow-1/"+modelA {
		t.Fatalf("weight lookups = %v, want only escrow-1", weights.lookups)
	}
	reads := map[string]bool{"LatestNonce": true, "GroupSize": true, "SlotParticipants": true}
	for _, escrow := range escrows.byModel[modelA] {
		for _, call := range escrow.Session.(*fakeSession).calls {
			if !reads[call] {
				t.Fatalf("escrow %q session saw %q", escrow.ID, call)
			}
		}
	}
}

type exhaustionReport struct {
	escrowID string
	reason   ExhaustionReason
}

// Test flow:
//  1. For each table case of an optional pin, a chain snapshot's nonce cap, and a spent candidate's balance, pick an escrow beside a fresh one.
//  2. Assert the picked escrow or error matches the case.
//  3. Assert the out-of-funds classification of the error matches the case.
//  4. Assert the exhaustion reports match the case: the chain's own cap reports `ExhaustionNonceCap` and outranks a dry balance, a dry balance alone is never reported, a pinned escrow is never reported for its balance, a pinned escrow that cannot pay this attempt is short even past the fallback ceiling, and the fallback ceiling alone reports nothing.
func TestPickEscrowReportsAnExhaustedEscrowButNeverForTheFallbackCeilingAlone(t *testing.T) {
	t.Parallel()

	chainCap := chain.PhaseSnapshot{MaxNonce: 1_000}
	const funded, dry uint64 = 1 << 30, 100
	testCases := []struct {
		name           string
		pinned         string
		snapshot       chain.PhaseSnapshot
		spentBalance   uint64
		request        RequestProfile
		wantPicked     string
		wantErr        error
		wantOutOfFunds bool
		wantReported   []exhaustionReport
	}{
		{
			name: "the chain's cap reports the spent candidate", snapshot: chainCap, spentBalance: funded,
			wantPicked:   "escrow-fresh",
			wantReported: []exhaustionReport{{escrowID: "escrow-spent", reason: ExhaustionNonceCap}},
		},
		{
			name: "the chain's cap reports a pinned spent escrow", pinned: "escrow-spent", snapshot: chainCap, spentBalance: funded,
			wantErr:      ErrNoEscrowCapacity,
			wantReported: []exhaustionReport{{escrowID: "escrow-spent", reason: ExhaustionNonceCap}},
		},
		{
			name: "the chain's cap outranks a dry balance", pinned: "escrow-spent", snapshot: chainCap, spentBalance: dry,
			wantErr:      ErrNoEscrowCapacity,
			wantReported: []exhaustionReport{{escrowID: "escrow-spent", reason: ExhaustionNonceCap}},
		},
		{
			name: "the fallback ceiling declines the spent candidate unreported", spentBalance: funded,
			wantPicked: "escrow-fresh",
		},
		{
			name: "the fallback ceiling declines a pinned spent escrow unreported", pinned: "escrow-spent", spentBalance: funded,
			wantErr: ErrNoEscrowCapacity,
		},
		{
			name: "past the fallback ceiling a dry candidate is declined unreported", spentBalance: dry,
			wantPicked: "escrow-fresh",
		},
		{
			name: "past the fallback ceiling a pinned dry escrow is declined unreported", pinned: "escrow-spent", spentBalance: dry,
			wantErr: ErrNoEscrowCapacity,
		},
		{
			name: "past the fallback ceiling a pinned escrow that cannot pay this attempt is short before the ceiling applies", pinned: "escrow-spent", spentBalance: dry,
			request: RequestProfile{InputBytes: 8_192, OutputTokens: 1_000},
			wantErr: ErrPinnedEscrowShort, wantOutOfFunds: true,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			settings := config.Defaults()
			settings.Limits.MaxTokensCap = 4_096
			scheduler, _, _ := newScheduler(
				candidate{id: "escrow-spent", weight: 100, latestNonce: fallbackNonceCeiling, balance: testCase.spentBalance, tokenPrice: 10},
				candidate{id: "escrow-fresh", weight: 100, latestNonce: 1, balance: funded, tokenPrice: 10},
			)
			scheduler.settings = config.NewHolder(&settings)
			var reported []exhaustionReport
			scheduler.onEscrowExhausted = func(escrowID string, reason ExhaustionReason) {
				reported = append(reported, exhaustionReport{escrowID: escrowID, reason: reason})
			}

			profile := testCase.request
			profile.Model, profile.Escrow = modelA, testCase.pinned
			picked, err := pickWith(scheduler, profile, testCase.snapshot)

			if picked.ID != testCase.wantPicked || !errors.Is(err, testCase.wantErr) {
				t.Fatalf("pickEscrow() = %q, %v; want %q, %v", picked.ID, err, testCase.wantPicked, testCase.wantErr)
			}
			if outOfFunds := errors.Is(err, types.ErrInsufficientBalance); outOfFunds != testCase.wantOutOfFunds {
				t.Fatalf("out of funds = %v, want %v: %v", outOfFunds, testCase.wantOutOfFunds, err)
			}
			if !slices.Equal(reported, testCase.wantReported) {
				t.Fatalf("reported = %v, want %v", reported, testCase.wantReported)
			}
		})
	}
}

// Test flow:
//  1. Build a spent candidate past the fallback ceiling and a fresh one, with no exhaustion reporter set.
//  2. Pick an escrow.
//  3. Assert the pick succeeds and returns the fresh escrow, so a nil reporter does not break the pick.
func TestPickEscrowWithoutANonceExhaustedReporterStillPicks(t *testing.T) {
	t.Parallel()
	scheduler, _, _ := newScheduler(
		candidate{id: "escrow-spent", weight: 100, latestNonce: fallbackNonceCeiling},
		candidate{id: "escrow-fresh", weight: 100, latestNonce: 1},
	)

	picked, err := pickWith(scheduler, RequestProfile{Model: modelA}, chain.PhaseSnapshot{})
	if err != nil || picked.ID != "escrow-fresh" {
		t.Fatalf("pickEscrow() = %q, %v; want escrow-fresh with a nil reporter", picked.ID, err)
	}
}

// Test flow:
//  1. Build one candidate whose latest nonce is past the fallback ceiling, and pin the request to it.
//  2. Pick an escrow.
//  3. Assert the pick fails with `ErrNoEscrowCapacity`, since the pinned path still honours the nonce ceiling.
func TestAPinnedEscrowStillHonoursTheNonceCeiling(t *testing.T) {
	t.Parallel()
	scheduler, _, _ := newScheduler(candidate{id: "escrow-1", weight: 1, latestNonce: 19_900})

	_, err := pickWith(scheduler, RequestProfile{Model: modelA, Escrow: "escrow-1"}, chain.PhaseSnapshot{})

	if !errors.Is(err, ErrNoEscrowCapacity) {
		t.Fatalf("pickEscrow() = %v, want the exhausted escrow refused", err)
	}
}

// Test flow:
//  1. For each table case of the pinned candidate's latest nonce, one below and one at the governance cutoff, pick the pinned escrow.
//  2. Assert the escrow one nonce below the cutoff is served.
//  3. Assert the escrow at the cutoff is refused with `ErrNoEscrowCapacity`.
func TestAPinnedEscrowMeetsTheInFlightMarginAtAFetchedCap(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name        string
		latestNonce uint64
		want        string
		wantErr     error
	}{
		{name: "one nonce below the cutoff is served", latestNonce: 794, want: "escrow-1"},
		{name: "at the cutoff is refused", latestNonce: 795, wantErr: ErrNoEscrowCapacity},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			scheduler, _, _ := newScheduler(candidate{id: "escrow-1", weight: 1, latestNonce: testCase.latestNonce, groupSize: 4})

			picked, err := pickWith(scheduler, RequestProfile{Model: modelA, Escrow: "escrow-1"}, chain.PhaseSnapshot{MaxNonce: 1_000})

			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("pickEscrow() = %v, want %v", err, testCase.wantErr)
			}
			if picked.ID != testCase.want {
				t.Fatalf("picked %q, want %q", picked.ID, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Build one candidate with a low latest nonce, and pin the request to it.
//  2. Pick an escrow.
//  3. Assert the pinned escrow is served.
func TestAPinnedEscrowUnderTheCeilingIsServed(t *testing.T) {
	t.Parallel()
	scheduler, _, _ := newScheduler(candidate{id: "escrow-1", weight: 1, latestNonce: 10})

	picked, err := pickWith(scheduler, RequestProfile{Model: modelA, Escrow: "escrow-1"}, chain.PhaseSnapshot{})

	if err != nil || picked.ID != "escrow-1" {
		t.Fatalf("pickEscrow() = %v, %v, want the pinned escrow served", picked.ID, err)
	}
}

func (f *fakeSession) Balance() uint64     { return f.balance }
func (f *fakeSession) TokenPrice() uint64  { return f.tokenPrice }
func (f *fakeSession) FeePerNonce() uint64 { return f.feePerNonce }

// Test flow:
//  1. Build three escrows priced so one request reserves 200: a poor one holding 150 beside 4 in-flight requests, a rich one, and an empty one that cannot afford even one.
//  2. Check `belowBalanceFloor` against a 20-token reserve for each.
//  3. Assert the poor escrow is below the floor, the rich one is not, and the empty one is.
func TestPickEscrowSkipsAnEscrowBelowItsBalanceFloor(t *testing.T) {
	t.Parallel()
	const reserveTokens, price = 20, 10

	poor := Escrow{ID: "poor", Session: &fakeSession{balance: 150, tokenPrice: price}, ActiveUsers: 4}
	rich := Escrow{ID: "rich", Session: &fakeSession{balance: 1 << 30, tokenPrice: price}, ActiveUsers: 4}
	single := Escrow{ID: "empty", Session: &fakeSession{balance: 100, tokenPrice: price}, ActiveUsers: 0}

	if !belowBalanceFloor(poor, reserveTokens) {
		t.Fatal("an escrow holding 150 against a 200 request was kept in routing")
	}
	if belowBalanceFloor(rich, reserveTokens) {
		t.Fatal("a funded escrow was taken out of routing")
	}
	if !belowBalanceFloor(single, reserveTokens) {
		t.Fatal("an escrow that cannot afford one request was kept in routing")
	}
}

// Test flow:
//  1. Build two escrows with the same balance but different token prices: cheap and dear.
//  2. Check `belowBalanceFloor` against a 100-token reserve for each.
//  3. Assert the cheap escrow, affording ten more requests at its price, is not below the floor.
//  4. Assert the dear escrow, unable to afford one request at its price, is below the floor.
func TestTheBalanceFloorIsPricedByTheEscrowsOwnTokenPrice(t *testing.T) {
	t.Parallel()
	const reserveTokens = 100

	cheap := Escrow{ID: "cheap", Session: &fakeSession{balance: 5_000, tokenPrice: 10}, ActiveUsers: 0}
	dear := Escrow{ID: "dear", Session: &fakeSession{balance: 5_000, tokenPrice: 100}, ActiveUsers: 0}

	if belowBalanceFloor(cheap, reserveTokens) {
		t.Fatal("an escrow that affords ten more requests at its own price was taken out of routing")
	}
	if !belowBalanceFloor(dear, reserveTokens) {
		t.Fatal("an escrow that cannot afford one request at its own price was kept in routing")
	}
}

// Test flow:
//  1. Price one request at 200 for its reserve and 50 for the nonce it draws.
//  2. Check `belowBalanceFloor` for an idle escrow one unit short of 250 and one holding exactly 250, then for a busy one the same, its in-flight request already reserved out of its balance.
//  3. Assert each short escrow is below the floor and each exact one is not, so the floor charges the per-nonce fee the chain charges and never the requests already on the escrow.
func TestTheBalanceFloorCountsThePerNonceFee(t *testing.T) {
	t.Parallel()
	const reserveTokens, price, fee = 20, 10, 50

	testCases := []struct {
		name        string
		balance     uint64
		activeUsers int
		wantBelow   bool
	}{
		{name: "idle, short of the fee", balance: 249, wantBelow: true},
		{name: "idle, covering the fee", balance: 250, wantBelow: false},
		{name: "busy, short of the fee", balance: 249, activeUsers: 1, wantBelow: true},
		{name: "busy, covering the fee", balance: 250, activeUsers: 1, wantBelow: false},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			candidate := Escrow{ID: "escrow", Session: &fakeSession{balance: testCase.balance, tokenPrice: price, feePerNonce: fee}, ActiveUsers: testCase.activeUsers}

			if below := belowBalanceFloor(candidate, reserveTokens); below != testCase.wantBelow {
				t.Fatalf("belowBalanceFloor() = %v, want %v", below, testCase.wantBelow)
			}
		})
	}
}

// Test flow:
//  1. Build an escrow priced so one request reserves 200, holding 300 once the three requests on it have reserved theirs.
//  2. Pick an escrow for one such request, then pin the same request to the escrow the way a hedge does.
//  3. Assert both picks serve the escrow: its balance is already net of the requests on it, and it still pays this one.
func TestAnEscrowServesTheRequestItsBalancePaysWhateverIsAlreadyOnIt(t *testing.T) {
	t.Parallel()
	scheduler, _, _ := newScheduler(candidate{id: "busy", activeUsers: 3, weight: 10, latestNonce: 1, balance: 300, tokenPrice: 10})
	profile := RequestProfile{Model: modelA, InputBytes: 10, OutputTokens: 10}

	picked, err := pickWith(scheduler, profile, chain.PhaseSnapshot{})
	if err != nil || picked.ID != "busy" {
		t.Fatalf("pickEscrow() = %q, %v, want the busy escrow served", picked.ID, err)
	}

	profile.Escrow = "busy"
	picked, err = pickWith(scheduler, profile, chain.PhaseSnapshot{})
	if err != nil || picked.ID != "busy" {
		t.Fatalf("pickEscrow(pinned) = %q, %v, want the busy escrow served", picked.ID, err)
	}
}

// Test flow:
//  1. Build an escrow priced at the maximum uint64 token price.
//  2. Check `belowBalanceFloor` against a reserve so large the price calculation would overflow.
//  3. Assert the escrow is below the floor, rather than the overflow wrapping into a small affordable number.
func TestABalanceFloorThatOverflowsTakesTheEscrowOutOfRouting(t *testing.T) {
	t.Parallel()
	ruinous := Escrow{ID: "ruinous", Session: &fakeSession{balance: math.MaxUint64, tokenPrice: math.MaxUint64}, ActiveUsers: 0}

	if !belowBalanceFloor(ruinous, 1<<62) {
		t.Fatal("a reserve too large to represent was read as affordable")
	}
}

// Test flow:
//  1. Check `belowBalanceFloor` with a zero reserve against an escrow with zero balance and 99 active users.
//  2. Assert it is not below the floor, since an unpriced floor must not eject anyone.
func TestBalanceFloorIsInertWithoutAReserve(t *testing.T) {
	t.Parallel()
	if belowBalanceFloor(Escrow{ID: "any", Session: &fakeSession{balance: 0, tokenPrice: 10}, ActiveUsers: 99}, 0) {
		t.Fatal("an unpriced floor took an escrow out of routing")
	}
}

// Test flow:
//  1. For each table case of a request profile, call `requestReserve`.
//  2. Assert the result matches the case: input bytes rather than the char/4 window estimate, and the request's own reserved output tokens even far above the global cap.
func TestARequestIsPricedTheWayTheChainChargesIt(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		profile RequestProfile
		want    uint64
	}{
		{
			name:    "the body's bytes, not the char/4 estimate the windows use",
			profile: RequestProfile{InputBytes: 8_192, InputTokens: 2_048},
			want:    8_192,
		},
		{
			name:    "the answer this request reserved, which a per-model cap or an admin request may raise far above the global one",
			profile: RequestProfile{InputBytes: 4_000, OutputTokens: 1_000_000},
			want:    1_004_000,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := requestReserve(testCase.profile); got != testCase.want {
				t.Fatalf("requestReserve = %d, want %d", got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Build a scheduler configured with a max-tokens cap of 4096.
//  2. Call its `retirementReserve` for a model the chain registers at 180 000 tokens, for one at 512, and for one it does not name.
//  3. Assert the first is priced at its context length in prompt bytes, the other two at the answer cap, reading nothing from any arriving request.
//  4. Assert an unconfigured scheduler's `retirementReserve` returns 0.
func TestTheRetirementPriceIsTheModelsContextAndNeverLessThanOneCappedAnswer(t *testing.T) {
	t.Parallel()
	settings := config.Defaults()
	settings.Limits.MaxTokensCap = 4_096
	priced := &Scheduler{settings: config.NewHolder(&settings)}
	snapshot := chain.PhaseSnapshot{Models: map[string]chain.ModelParams{
		modelA: {MaxModelLen: fullContextTokens}, "short-model": {MaxModelLen: 512},
	}}

	if got := priced.retirementReserve(modelA, snapshot); got != fullContextReserve {
		t.Fatalf("retirementReserve(%s) = %d, want the model's context length in prompt bytes", modelA, got)
	}
	if got := priced.retirementReserve("short-model", snapshot); got != 4_096 {
		t.Fatalf("retirementReserve(short-model) = %d, want the answer cap above a shorter context", got)
	}
	if got := priced.retirementReserve("unnamed-model", snapshot); got != 4_096 {
		t.Fatalf("retirementReserve(unnamed) = %d, want the answer cap alone", got)
	}
	if got := (&Scheduler{}).retirementReserve(modelA, snapshot); got != 0 {
		t.Fatalf("retirementReserve = %d before any configuration loaded, want an unpriced 0", got)
	}
}

// schedulerWithAllowlist wires the settings the picker reads, which the plain constructor leaves nil.
func schedulerWithAllowlist(t *testing.T, allowlist []string, groups map[string][]string) *Scheduler {
	t.Helper()
	configuration := config.Defaults()
	configuration.Scheduler.ParticipantAllowlist = allowlist
	holder := &config.Holder{}
	holder.Swap(&configuration)

	escrows := &fakeEscrows{byModel: map[string][]Escrow{}}
	weights := &fakeWeights{byEscrow: map[string]float64{}}
	for _, escrowID := range slices.Sorted(maps.Keys(groups)) {
		escrows.byModel[modelA] = append(escrows.byModel[modelA], Escrow{
			ID:      escrowID,
			Model:   modelA,
			Session: &fakeSession{slots: groups[escrowID], participants: groups[escrowID]},
		})
		weights.byEscrow[escrowID] = 1
	}
	return &Scheduler{
		escrows:     escrows,
		capacity:    weights,
		settings:    holder,
		limiter:     newFakeLimiter(),
		perf:        &fakePerf{ejected: map[string]bool{}},
		dispatchers: map[string]*dispatcher{},
	}
}

// Test flow:
//  1. Build a scheduler with an allowlist naming one participant, and two escrows: one whose group holds nobody allowed, one whose group is that one allowed participant.
//  2. Pick an escrow.
//  3. Assert the escrow with an allowed participant is picked, since the disallowed one is never a candidate at all.
func TestPickEscrowSkipsAnEscrowHoldingNoAllowedParticipant(t *testing.T) {
	scheduler := schedulerWithAllowlist(t, []string{"allowed"}, map[string][]string{
		"crowded": {"someone-else", "another"},
		"lonely":  {"allowed"},
	})

	picked, err := pickWith(scheduler, RequestProfile{Model: modelA}, chain.PhaseSnapshot{})
	if err != nil {
		t.Fatalf("pickEscrow(): %v", err)
	}
	if picked.ID != "lonely" {
		t.Fatalf("picked %q, want the only escrow whose group holds an allowed participant", picked.ID)
	}
}

// Test flow:
//  1. Build a scheduler with an allowlist naming a participant no escrow's group holds.
//  2. Pick an escrow.
//  3. Assert the pick fails with `ErrAllowlistUnreachable`.
func TestPickEscrowReportsAnAllowlistNoEscrowCanReach(t *testing.T) {
	scheduler := schedulerWithAllowlist(t, []string{"nobody-holds-this"}, map[string][]string{
		"one": {"someone-else"},
		"two": {"another"},
	})

	_, err := pickWith(scheduler, RequestProfile{Model: modelA}, chain.PhaseSnapshot{})

	if !errors.Is(err, ErrAllowlistUnreachable) {
		t.Fatalf("pickEscrow() = %v, want ErrAllowlistUnreachable", err)
	}
}

// Test flow:
//  1. Build a scheduler with no allowlist configured.
//  2. Pick an escrow.
//  3. Assert the pick succeeds.
func TestPickEscrowIgnoresTheAllowlistWhenItIsEmpty(t *testing.T) {
	scheduler := schedulerWithAllowlist(t, nil, map[string][]string{"one": {"anybody"}})

	if _, err := pickWith(scheduler, RequestProfile{Model: modelA}, chain.PhaseSnapshot{}); err != nil {
		t.Fatalf("pickEscrow() with no allowlist: %v", err)
	}
}

type reserveTakenRecorder struct {
	mu    sync.Mutex
	taken []string
}

func (r *reserveTakenRecorder) record(escrowID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.taken = append(r.taken, escrowID)
}

func (r *reserveTakenRecorder) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.taken)
}

const (
	reserveTestPrice      = 10
	reserveTestSmallBytes = 1_000
	reserveTestLargeBytes = 400_000
)

// schedulerWithReserve prices a small request at 10 000 and a large one at 4 000 000.
func schedulerWithReserve(candidates ...candidate) (*Scheduler, *reserveTakenRecorder) {
	scheduler, _, _ := newScheduler(candidates...)
	recorder := &reserveTakenRecorder{}
	scheduler.onReserveTaken = recorder.record
	return scheduler, recorder
}

// Test flow:
//  1. Build a busy regular escrow and an idle, full reserve escrow.
//  2. Pick an escrow for a small request.
//  3. Assert the regular is picked although the reserve scores better, and no reserve was reported taken.
func TestAReserveIsNotPickedWhileARegularCanServe(t *testing.T) {
	t.Parallel()
	scheduler, recorder := schedulerWithReserve(
		candidate{id: "regular", activeUsers: 5, weight: 1, balance: 1 << 30, tokenPrice: reserveTestPrice},
		candidate{id: "reserve", weight: 1, balance: 1 << 30, tokenPrice: reserveTestPrice, isReserve: true},
	)

	picked, err := pickWith(scheduler, RequestProfile{Model: modelA, InputBytes: reserveTestSmallBytes}, chain.PhaseSnapshot{})

	if err != nil || picked.ID != "regular" {
		t.Fatalf("picked %q, err %v; want the regular", picked.ID, err)
	}
	if taken := recorder.recorded(); len(taken) != 0 {
		t.Fatalf("reserves taken = %v, want none", taken)
	}
}

// Test flow:
//  1. Build two regular escrows whose balance cannot cover a large request, and a full reserve that can.
//  2. Pick an escrow for the large request.
//  3. Assert the reserve is picked, and nothing is reported taken yet: the take waits for the nonce.
func TestALargeRequestNoRegularCanAffordTakesTheReserve(t *testing.T) {
	t.Parallel()
	scheduler, recorder := schedulerWithReserve(
		candidate{id: "regular-1", weight: 1, balance: 1_000_000, tokenPrice: reserveTestPrice},
		candidate{id: "regular-2", weight: 1, balance: 1_000_000, tokenPrice: reserveTestPrice},
		candidate{id: "reserve", weight: 1, balance: 10_000_000, tokenPrice: reserveTestPrice, isReserve: true},
	)

	picked, err := pickWith(scheduler, RequestProfile{Model: modelA, InputBytes: reserveTestLargeBytes}, chain.PhaseSnapshot{})

	if err != nil || picked.ID != "reserve" {
		t.Fatalf("picked %q, err %v; want the reserve", picked.ID, err)
	}
	if taken := recorder.recorded(); len(taken) != 0 {
		t.Fatalf("reserves taken at pick time = %v, want none: the take is reported once a nonce is served", taken)
	}
}

// Test flow:
//  1. Build one regular escrow and one reserve, neither able to cover a large request.
//  2. Pick an escrow.
//  3. Assert the pick fails with ErrNoEscrowCapacity wrapping ErrInsufficientBalance, and no reserve was reported taken.
func TestAReserveTooSmallForTheRequestIsNotTaken(t *testing.T) {
	t.Parallel()
	scheduler, recorder := schedulerWithReserve(
		candidate{id: "regular", weight: 1, balance: 1_000_000, tokenPrice: reserveTestPrice},
		candidate{id: "reserve", weight: 1, balance: 1_000_000, tokenPrice: reserveTestPrice, isReserve: true},
	)

	_, err := pickWith(scheduler, RequestProfile{Model: modelA, InputBytes: reserveTestLargeBytes}, chain.PhaseSnapshot{})

	if !errors.Is(err, ErrNoEscrowCapacity) || !errors.Is(err, types.ErrInsufficientBalance) {
		t.Fatalf("err = %v, want no capacity for insufficient balance", err)
	}
	if taken := recorder.recorded(); len(taken) != 0 {
		t.Fatalf("reserves taken = %v, want none", taken)
	}
}

// Test flow:
//  1. Pin a request to a reserve escrow that can cover it.
//  2. Pick an escrow.
//  3. Assert the pinned reserve is returned, and nothing is reported taken yet.
func TestAPinnedReserveIsPicked(t *testing.T) {
	t.Parallel()
	scheduler, recorder := schedulerWithReserve(
		candidate{id: "regular", weight: 1, balance: 1 << 30, tokenPrice: reserveTestPrice},
		candidate{id: "reserve", weight: 1, balance: 1 << 30, tokenPrice: reserveTestPrice, isReserve: true},
	)

	picked, err := pickWith(scheduler, RequestProfile{Model: modelA, Escrow: "reserve", InputBytes: reserveTestSmallBytes}, chain.PhaseSnapshot{})

	if err != nil || picked.ID != "reserve" {
		t.Fatalf("picked %q, err %v; want the pinned reserve", picked.ID, err)
	}
	if taken := recorder.recorded(); len(taken) != 0 {
		t.Fatalf("reserves taken at pick time = %v, want none", taken)
	}
}

// Test flow:
//  1. Build a regular escrow with an unusable weight and a full reserve.
//  2. Pick an escrow.
//  3. Assert the pick finds no capacity rather than the reserve: a regular passed over for its hosts is not one that could not pay.
func TestARegularPassedOverForItsHostsDoesNotOpenTheReserve(t *testing.T) {
	t.Parallel()
	scheduler, _ := schedulerWithReserve(
		candidate{id: "regular", weight: 0, balance: 1 << 30, tokenPrice: reserveTestPrice},
		candidate{id: "reserve", weight: 1, balance: 1 << 30, tokenPrice: reserveTestPrice, isReserve: true},
	)

	_, err := pickWith(scheduler, RequestProfile{Model: modelA, InputBytes: reserveTestSmallBytes}, chain.PhaseSnapshot{})

	if !errors.Is(err, ErrNoEscrowCapacity) {
		t.Fatalf("err = %v, want ErrNoEscrowCapacity", err)
	}
}

// Test flow:
//  1. Configure an allowlist that rules out the regular escrow's whole group but admits the reserve's.
//  2. Pick an escrow.
//  3. Assert the pick reports the allowlist as unreachable rather than taking the reserve: an operator's host restriction is not a regular failing for money.
func TestAnAllowlistThatRulesOutTheRegularsDoesNotOpenTheReserve(t *testing.T) {
	scheduler := schedulerWithAllowlist(t, []string{"allowed"}, map[string][]string{
		"regular": {"someone-else"},
		"reserve": {"allowed"},
	})
	for index, candidate := range scheduler.escrows.(*fakeEscrows).byModel[modelA] {
		if candidate.ID == "reserve" {
			scheduler.escrows.(*fakeEscrows).byModel[modelA][index].IsReserve = true
		}
	}

	picked, err := pickWith(scheduler, RequestProfile{Model: modelA}, chain.PhaseSnapshot{})

	if !errors.Is(err, ErrAllowlistUnreachable) {
		t.Fatalf("pickEscrow() = %q, %v; want ErrAllowlistUnreachable", picked.ID, err)
	}
}

// Test flow:
//  1. Build a busy regular, a regular that refused for insufficient balance, and a full reserve.
//  2. Pick with both regulars avoided, each for its own reason, as the busy re-pick's walk past empty escrows does.
//  3. Assert the reserve stays shut: one regular gave up for its hosts, so the reserves are not what this request lacks.
func TestABusyRegularKeepsTheReserveShutBesideOneThatCannotPay(t *testing.T) {
	t.Parallel()
	scheduler, _ := schedulerWithReserve(
		candidate{id: "busy", weight: 1, balance: 1 << 30, tokenPrice: reserveTestPrice},
		candidate{id: "broke", weight: 1, balance: 1 << 30, tokenPrice: reserveTestPrice},
		candidate{id: "reserve", weight: 1, balance: 1 << 30, tokenPrice: reserveTestPrice, isReserve: true},
	)
	profile := RequestProfile{Model: modelA, InputBytes: reserveTestSmallBytes}

	_, err := scheduler.pickEscrow(profile, chain.PhaseSnapshot{}, newWaiter(profile, time.Time{}), avoidedEscrows{"busy": avoidedHostsBusy, "broke": avoidedOutOfFunds})

	if !errors.Is(err, ErrNoEscrowCapacity) {
		t.Fatalf("err = %v, want ErrNoEscrowCapacity", err)
	}
}

// Test flow:
//  1. Build a regular escrow past the chain's nonce cap and a full reserve.
//  2. Pick an escrow.
//  3. Assert the reserve is picked: a regular whose nonce budget is spent failed for money, which is what the reserve covers.
func TestARegularPastItsNonceCapOpensTheReserve(t *testing.T) {
	t.Parallel()
	scheduler, _ := schedulerWithReserve(
		candidate{id: "spent", weight: 1, latestNonce: 1_000_000, balance: 1 << 30, tokenPrice: reserveTestPrice},
		candidate{id: "reserve", weight: 1, balance: 1 << 30, tokenPrice: reserveTestPrice, isReserve: true},
	)

	picked, err := pickWith(scheduler, RequestProfile{Model: modelA, InputBytes: reserveTestSmallBytes}, chain.PhaseSnapshot{MaxNonce: 1_000})

	if err != nil || picked.ID != "reserve" {
		t.Fatalf("picked %q, err %v; want the reserve", picked.ID, err)
	}
}

// Test flow:
//  1. Build a thin escrow that affords one attempt of a 200-token request and scores best, beside a busier escrow that affords every attempt.
//  2. Pick an escrow and assert the one that affords every attempt wins despite its score.
//  3. Build the thin escrow alone, pick again, and assert it still serves.
func TestPickPrefersAnEscrowThatAffordsEveryAttempt(t *testing.T) {
	t.Parallel()
	profile := RequestProfile{Model: modelA, InputBytes: 100, OutputTokens: 100}
	thin := candidate{id: "thin", weight: 100, latestNonce: 1, balance: 300, tokenPrice: 1}
	funded := candidate{id: "funded", activeUsers: 1, weight: 100, latestNonce: 1, balance: 1_000, tokenPrice: 1}

	both, _, _ := newScheduler(thin, funded)
	picked, err := pickWith(both, profile, chain.PhaseSnapshot{})
	if err != nil || picked.ID != "funded" {
		t.Fatalf("pickEscrow = %q, %v; want the escrow that affords every attempt", picked.ID, err)
	}

	alone, _, _ := newScheduler(thin)
	picked, err = pickWith(alone, profile, chain.PhaseSnapshot{})
	if err != nil || picked.ID != "thin" {
		t.Fatalf("pickEscrow = %q, %v; want the thin escrow when nothing affords more", picked.ID, err)
	}
}

// Test flow:
//  1. Build an idle escrow that affords two attempts of a 200-token request beside a busier one that affords three.
//  2. For each table case, set `engine_max_attempts_per_request` and pick an escrow.
//  3. Assert the setting decides the funded tier: at 0 and 2 both qualify and the idle one wins on score, at 3 only the busier one qualifies.
func TestPickFundsTheConfiguredAttempts(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		attempts int64
		want     string
	}{
		{name: "unbounded_counts_two", attempts: 0, want: "twice"},
		{name: "two_attempts", attempts: 2, want: "twice"},
		{name: "three_attempts", attempts: 3, want: "thrice"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			scheduler, _, _ := newScheduler(
				candidate{id: "twice", weight: 100, latestNonce: 1, balance: 400, tokenPrice: 1},
				candidate{id: "thrice", activeUsers: 1, weight: 100, latestNonce: 1, balance: 1_000, tokenPrice: 1},
			)
			settings := config.Defaults()
			settings.Limits.MaxTokensCap = 16
			settings.Engine.MaxAttemptsPerRequest = testCase.attempts
			scheduler.settings = config.NewHolder(&settings)

			picked, err := pickWith(scheduler, RequestProfile{Model: modelA, InputBytes: 100, OutputTokens: 100}, chain.PhaseSnapshot{})

			if err != nil || picked.ID != testCase.want {
				t.Fatalf("pickEscrow = %q, %v; want %q", picked.ID, err, testCase.want)
			}
		})
	}
}

// moneyShortRecorder counts the models the scheduler reported as money-short.
type moneyShortRecorder struct {
	mu     sync.Mutex
	models []string
}

func (recorder *moneyShortRecorder) record(model string) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.models = append(recorder.models, model)
}

func (recorder *moneyShortRecorder) recorded() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return slices.Clone(recorder.models)
}

// Test flow:
//  1. Table-driven: an unpinned request no escrow can pay, the same request pinned to that escrow, an unpinned request declined only by the fallback nonce ceiling, and an unpinned request a funded escrow serves.
//  2. Pick an escrow for each with a money-short recorder installed.
//  3. Assert only the unpinned request declined for money reported its model, once.
func TestOnlyAnUnpinnedPickDeclinedForMoneyReportsItsModelMoneyShort(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		candidate candidate
		profile   RequestProfile
		want      []string
	}{
		{
			name:      "an unpinned request no escrow can pay",
			candidate: candidate{id: "poor", weight: 1, balance: 50, tokenPrice: 10},
			profile:   RequestProfile{Model: modelA, InputBytes: 20},
			want:      []string{modelA},
		},
		{
			name:      "the same request pinned to that escrow",
			candidate: candidate{id: "poor", weight: 1, balance: 50, tokenPrice: 10},
			profile:   RequestProfile{Model: modelA, Escrow: "poor", InputBytes: 20},
		},
		{
			name:      "an escrow past the fallback nonce ceiling",
			candidate: candidate{id: "spent", weight: 1, balance: 1 << 30, tokenPrice: 10, latestNonce: fallbackNonceCeiling},
			profile:   RequestProfile{Model: modelA, InputBytes: 20},
		},
		{
			name:      "a funded escrow",
			candidate: candidate{id: "rich", weight: 1, balance: 1 << 30, tokenPrice: 10},
			profile:   RequestProfile{Model: modelA, InputBytes: 20},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			scheduler, _, _ := newScheduler(testCase.candidate)
			recorder := &moneyShortRecorder{}
			scheduler.onMoneyShort = recorder.record

			_, _ = pickWith(scheduler, testCase.profile, chain.PhaseSnapshot{})

			if got := recorder.recorded(); !slices.Equal(got, testCase.want) {
				t.Fatalf("money-short reports = %v, want %v", got, testCase.want)
			}
		})
	}
}
