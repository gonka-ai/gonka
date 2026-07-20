package observer_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"devshard/chainoracle/blocks"
	"devshard/chainoracle/blocks/observer"
	"devshard/chainoracle/blocks/verifier"
	"devshard/signing"

	"github.com/stretchr/testify/require"
)

// genValidators builds n signers and their 20-byte addresses; used as
// the default "10 mock validators" in most tests.
func genValidators(t *testing.T, n int) ([]observer.MockValidator, []verifier.Validator) {
	t.Helper()
	mocks := make([]observer.MockValidator, 0, n)
	verifiers := make([]verifier.Validator, 0, n)
	for i := 0; i < n; i++ {
		s, err := signing.GenerateKey()
		require.NoError(t, err)
		addr, err := blocks.AddressBytes(s.PublicKeyBytes())
		require.NoError(t, err)
		mocks = append(mocks, observer.MockValidator{Signer: s, Address: addr, Power: 1})
		verifiers = append(verifiers, verifier.Validator{Address: append([]byte(nil), addr...), Power: 1})
	}
	return mocks, verifiers
}

func newMockForTest(t *testing.T, seed int64) (*observer.Mock, *verifier.Verifier) {
	t.Helper()
	mocks, verifiers := genValidators(t, 10)

	m, err := observer.NewMock(observer.MockConfig{
		ChainID:       "gonka-test",
		Validators:    mocks,
		BlockInterval: 2 * time.Second,
		Seed:          seed,
		Start:         time.Unix(1_700_000_000, 0).UTC(),
		InitialHeight: 1,
	})
	require.NoError(t, err)

	vs, err := verifier.NewValidatorSet("gonka-test", verifiers)
	require.NoError(t, err)
	return m, verifier.New(vs)
}

func TestMockObserver_MonotonicHeight(t *testing.T) {
	m, v := newMockForTest(t, 42)

	var last int64
	for i := 0; i < 5; i++ {
		h, err := m.AdvanceOne()
		require.NoError(t, err)
		require.Greater(t, h.Height, last)
		require.NoError(t, v.Verify(h, last))
		last = h.Height
	}
}

func TestMockObserver_Cadence(t *testing.T) {
	m, _ := newMockForTest(t, 7)

	h1, err := m.AdvanceOne()
	require.NoError(t, err)
	h2, err := m.AdvanceOne()
	require.NoError(t, err)
	require.Equal(t, 2*time.Second, h2.Time.Sub(h1.Time))
}

func TestMockObserver_DeterministicForSameSeed(t *testing.T) {
	// Two observers with the same seed and same validator set must emit
	// byte-identical canonical bytes, including the same drop pattern.
	mocks, _ := genValidators(t, 10)

	cfg := observer.MockConfig{
		ChainID:       "gonka-test",
		Validators:    mocks,
		BlockInterval: time.Second,
		Seed:          123,
		Start:         time.Unix(1, 0).UTC(),
		InitialHeight: 1,
	}

	a, err := observer.NewMock(cfg)
	require.NoError(t, err)
	b, err := observer.NewMock(cfg)
	require.NoError(t, err)

	for i := 0; i < 4; i++ {
		ha, err := a.AdvanceOne()
		require.NoError(t, err)
		hb, err := b.AdvanceOne()
		require.NoError(t, err)
		require.True(t,
			bytes.Equal(blocks.CanonicalHeaderBytes(ha), blocks.CanonicalHeaderBytes(hb)),
			"canonical bytes diverged at height %d", ha.Height,
		)
		// The drop pattern (number of sigs) must also be identical.
		require.Equal(t, len(ha.Commit.Signatures), len(hb.Commit.Signatures))
	}
}

// TestMockObserver_MultiValidator_QuorumFloor asserts the contract the
// mock promises producers: every fabricated block has strictly > 3/4 of
// the total voting power in its commit, regardless of the height-level
// drop choices.
func TestMockObserver_MultiValidator_QuorumFloor(t *testing.T) {
	m, v := newMockForTest(t, 777)
	total := int64(0)
	for _, val := range []int64{1, 1, 1, 1, 1, 1, 1, 1, 1, 1} {
		total += val
	}

	seenFull := false
	seenPartial := false
	for i := 0; i < 200; i++ {
		h, err := m.AdvanceOne()
		require.NoError(t, err)
		require.NoError(t, v.Verify(h, h.Height-1))

		sigs := int64(len(h.Commit.Signatures))
		require.GreaterOrEqual(t, sigs, int64(1), "height %d: empty commit", h.Height)
		require.LessOrEqual(t, sigs, total, "height %d: more sigs than validators", h.Height)

		// Each commit must beat the > 3/4 threshold (i.e. need ≥ 8 sigs
		// of power 1 each when total is 10).
		require.Greater(t, 4*sigs, 3*total,
			"height %d: only %d of %d sigs retained", h.Height, sigs, total)

		if sigs == total {
			seenFull = true
		} else {
			seenPartial = true
		}
	}
	// Over 200 heights we should see both full and partial commits, so
	// the drop code actually runs (and consumers get realistic mixes).
	require.True(t, seenFull, "expected at least one fully-signed block")
	require.True(t, seenPartial, "expected at least one partially-signed block")
}

func TestMockObserver_Subscribe_FanOut(t *testing.T) {
	m, v := newMockForTest(t, 9)

	// Seed one header so subscribers can replay it.
	_, err := m.AdvanceOne()
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch1, err := m.Subscribe(ctx, 1)
	require.NoError(t, err)
	ch2, err := m.Subscribe(ctx, 1)
	require.NoError(t, err)

	drain := func(ch <-chan *blocks.Header) *blocks.Header {
		select {
		case h := <-ch:
			return h
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for header")
		}
		return nil
	}
	r1 := drain(ch1)
	r2 := drain(ch2)
	require.Equal(t, int64(1), r1.Height)
	require.Equal(t, int64(1), r2.Height)

	for i := 0; i < 3; i++ {
		_, err := m.AdvanceOne()
		require.NoError(t, err)
	}
	for want := int64(2); want <= 4; want++ {
		h1 := drain(ch1)
		h2 := drain(ch2)
		require.Equal(t, want, h1.Height)
		require.Equal(t, want, h2.Height)
		require.NoError(t, v.Verify(h1, want-1))
	}
}

func TestMockObserver_At_ReturnsHistory(t *testing.T) {
	m, _ := newMockForTest(t, 11)
	for i := 0; i < 3; i++ {
		_, err := m.AdvanceOne()
		require.NoError(t, err)
	}
	h, err := m.At(context.Background(), 2)
	require.NoError(t, err)
	require.Equal(t, int64(2), h.Height)

	_, err = m.At(context.Background(), 99)
	require.Error(t, err)
}

func TestMockObserver_Prove_StableForSameInput(t *testing.T) {
	m, _ := newMockForTest(t, 13)
	_, err := m.AdvanceOne()
	require.NoError(t, err)
	_, err = m.AdvanceOne()
	require.NoError(t, err)

	p1, err := m.Prove(context.Background(), "/escrow/42", 2)
	require.NoError(t, err)
	p2, err := m.Prove(context.Background(), "/escrow/42", 2)
	require.NoError(t, err)
	require.Equal(t, p1, p2)

	p3, err := m.Prove(context.Background(), "/escrow/43", 2)
	require.NoError(t, err)
	require.NotEqual(t, p1.Ops, p3.Ops)
}

func TestMockObserver_RejectsBadConfig(t *testing.T) {
	_, err := observer.NewMock(observer.MockConfig{ChainID: ""})
	require.Error(t, err)

	// Empty validator list is always invalid.
	_, err = observer.NewMock(observer.MockConfig{
		ChainID:    "gonka-test",
		Validators: nil,
	})
	require.Error(t, err)

	// Bad address length.
	s, err := signing.GenerateKey()
	require.NoError(t, err)
	_, err = observer.NewMock(observer.MockConfig{
		ChainID: "gonka-test",
		Validators: []observer.MockValidator{
			{Signer: s, Address: []byte{1, 2, 3}, Power: 1},
		},
	})
	require.Error(t, err)

	// Duplicate addresses rejected.
	addr, err := blocks.AddressBytes(s.PublicKeyBytes())
	require.NoError(t, err)
	_, err = observer.NewMock(observer.MockConfig{
		ChainID: "gonka-test",
		Validators: []observer.MockValidator{
			{Signer: s, Address: addr, Power: 1},
			{Signer: s, Address: addr, Power: 1},
		},
	})
	require.Error(t, err)
}

// TestMockObserver_PowerWeighted_HeavyAlwaysSigns pins a power
// distribution where one validator dominates (power 5 of total 10) and
// asserts the mock never drops it. maxDropPower = (10-1)/4 = 2, so any
// block missing the heavy validator would have at most 5 power
// remaining which fails the > 3/4 floor (requires > 7.5). The drop
// algorithm must recognize this and always pick from the light
// validators instead.
func TestMockObserver_PowerWeighted_HeavyAlwaysSigns(t *testing.T) {
	// 1 heavy (power 5) + 5 light (power 1) ⇒ total = 10.
	heavy, err := signing.GenerateKey()
	require.NoError(t, err)
	heavyAddr, err := blocks.AddressBytes(heavy.PublicKeyBytes())
	require.NoError(t, err)

	validators := []observer.MockValidator{
		{Signer: heavy, Address: heavyAddr, Power: 5},
	}
	verifiers := []verifier.Validator{
		{Address: append([]byte(nil), heavyAddr...), Power: 5},
	}
	for i := 0; i < 5; i++ {
		s, err := signing.GenerateKey()
		require.NoError(t, err)
		addr, err := blocks.AddressBytes(s.PublicKeyBytes())
		require.NoError(t, err)
		validators = append(validators, observer.MockValidator{Signer: s, Address: addr, Power: 1})
		verifiers = append(verifiers, verifier.Validator{Address: append([]byte(nil), addr...), Power: 1})
	}

	m, err := observer.NewMock(observer.MockConfig{
		ChainID:       "gonka-test",
		Validators:    validators,
		BlockInterval: time.Second,
		Seed:          314,
		InitialHeight: 1,
	})
	require.NoError(t, err)
	vs, err := verifier.NewValidatorSet("gonka-test", verifiers)
	require.NoError(t, err)
	v := verifier.New(vs)

	for i := 0; i < 200; i++ {
		h, err := m.AdvanceOne()
		require.NoError(t, err)

		// Heavy validator must appear in every commit.
		foundHeavy := false
		totalPower := int64(0)
		for _, sig := range h.Commit.Signatures {
			if bytes.Equal(sig.ValidatorAddress, heavyAddr) {
				foundHeavy = true
				totalPower += 5
			} else {
				totalPower += 1
			}
		}
		require.True(t, foundHeavy,
			"height %d: heavy validator dropped; would leave <= 5 power which violates > 3/4",
			h.Height)
		// Quorum floor: total remaining power > 3/4 of 10 ⇒ ≥ 8.
		require.Greater(t, 4*totalPower, 3*int64(10),
			"height %d: retained power %d violates > 3/4 floor", h.Height, totalPower)
		// Pinned verifier accepts every block.
		require.NoError(t, v.Verify(h, h.Height-1))
	}
}

// TestMockObserver_SignerRotation asserts the drop algorithm is not
// fixed: over many heights, every validator in the set is sometimes
// dropped. A stuck subset would indicate the per-height RNG is not
// mixing with the validator permutation.
func TestMockObserver_SignerRotation(t *testing.T) {
	mocks, _ := genValidators(t, 10)
	m, err := observer.NewMock(observer.MockConfig{
		ChainID:       "gonka-test",
		Validators:    mocks,
		BlockInterval: time.Second,
		Seed:          2024,
		InitialHeight: 1,
	})
	require.NoError(t, err)

	// Count how many blocks each validator is absent from.
	absences := make([]int, len(mocks))
	const blocks = 500
	for i := 0; i < blocks; i++ {
		h, err := m.AdvanceOne()
		require.NoError(t, err)
		present := make(map[string]struct{}, len(h.Commit.Signatures))
		for _, sig := range h.Commit.Signatures {
			present[string(sig.ValidatorAddress)] = struct{}{}
		}
		for j, v := range mocks {
			if _, ok := present[string(v.Address)]; !ok {
				absences[j]++
			}
		}
	}
	// Every validator must be absent from at least one block; otherwise
	// the drop set is stuck on a fixed subset.
	for j, count := range absences {
		require.Greater(t, count, 0,
			"validator %d never dropped across %d blocks — rotation stuck", j, blocks)
	}
}

// TestMockObserver_SingleValidator_FullSign confirms that when the set
// is too small for drops to stay above the > 3/4 floor (N ≤ 3), the
// mock always signs with every validator.
func TestMockObserver_SingleValidator_FullSign(t *testing.T) {
	for _, n := range []int{1, 2, 3} {
		mocks, verifiers := genValidators(t, n)
		m, err := observer.NewMock(observer.MockConfig{
			ChainID:       "gonka-test",
			Validators:    mocks,
			BlockInterval: time.Second,
			Seed:          1,
			InitialHeight: 1,
		})
		require.NoError(t, err)
		vs, err := verifier.NewValidatorSet("gonka-test", verifiers)
		require.NoError(t, err)
		v := verifier.New(vs)

		for i := 0; i < 20; i++ {
			h, err := m.AdvanceOne()
			require.NoError(t, err)
			require.Equal(t, n, len(h.Commit.Signatures),
				"n=%d height=%d: expected full commit when drop budget is 0", n, h.Height)
			require.NoError(t, v.Verify(h, h.Height-1))
		}
	}
}
