package observer

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"devshard/chainoracle/blocks"
	"devshard/signing"
)

// MockValidator pins one participant of the fabricated validator set.
// Signer produces the commit signature; Address is the 20-byte address
// derived from Signer.PublicKeyBytes(); Power is the voting power used to
// compute the > 3/4 quorum floor.
type MockValidator struct {
	Signer  *signing.Secp256k1Signer
	Address []byte
	Power   int64
}

// MockConfig pins a deterministic, fabricated-header observer.
//
// BlockInterval controls Run cadence; tests drive the observer with
// AdvanceOne instead. Same Seed + same Validators produce byte-identical
// headers, which the §8.1 determinism test depends on.
//
// The mock simulates a multi-validator Cosmos chain: every block is
// multi-signed by (most of) the pinned validator set. Each block
// deliberately omits a deterministic pseudorandom subset of signatures
// so consumers exercise the partial-quorum code path, while guaranteeing
// the retained power is strictly > 3/4 of the total (stricter than the
// verifier's > 2/3 rule to leave headroom).
type MockConfig struct {
	ChainID       string
	Validators    []MockValidator
	BlockInterval time.Duration
	// BlockIntervalDelta adds symmetric jitter around BlockInterval.
	// Example: 1s ± 250ms => [750ms, 1250ms]. ≤0 disables jitter.
	BlockIntervalDelta time.Duration
	Seed          int64
	Start         time.Time
	InitialHeight int64 // default 1
}

// Mock is a testenv-only observer that fabricates signed block headers on
// a fixed cadence. It implements both observer.Observer and
// blocks.BlockOracle, so height-sync can mount it directly with
// server.Mount.
type Mock struct {
	cfg        MockConfig
	totalPower int64
	// maxDropPower is the largest total power the mock may remove from a
	// block while still keeping the remainder strictly > 3/4 of total.
	maxDropPower int64

	mu      sync.RWMutex
	latest  *blocks.Header
	history map[int64]*blocks.Header
	subs    map[int]*subscription
	nextSub int
	closed  bool
}

type subscription struct {
	ch   chan *blocks.Header
	from int64
}

// NewMock constructs a mock observer. The constructor validates the
// configuration but does not produce any headers; call AdvanceOne for
// one-shot tests or Run for the cadence loop.
func NewMock(cfg MockConfig) (*Mock, error) {
	if cfg.ChainID == "" {
		return nil, errors.New("mock observer: empty chain id")
	}
	if len(cfg.Validators) == 0 {
		return nil, errors.New("mock observer: at least one validator is required")
	}
	var total int64
	seen := make(map[string]struct{}, len(cfg.Validators))
	validators := make([]MockValidator, len(cfg.Validators))
	for i, v := range cfg.Validators {
		if v.Signer == nil {
			return nil, fmt.Errorf("mock observer: validator[%d] has nil signer", i)
		}
		if len(v.Address) != 20 {
			return nil, fmt.Errorf("mock observer: validator[%d] address must be 20 bytes, got %d",
				i, len(v.Address))
		}
		if v.Power <= 0 {
			return nil, fmt.Errorf("mock observer: validator[%d] power must be > 0", i)
		}
		key := string(v.Address)
		if _, dup := seen[key]; dup {
			return nil, fmt.Errorf("mock observer: validator[%d] duplicate address %x", i, v.Address)
		}
		seen[key] = struct{}{}
		validators[i] = MockValidator{
			Signer:  v.Signer,
			Address: append([]byte(nil), v.Address...),
			Power:   v.Power,
		}
		total += v.Power
	}
	cfg.Validators = validators

	if cfg.BlockInterval <= 0 {
		cfg.BlockInterval = time.Second
	}
	if cfg.BlockIntervalDelta < 0 {
		cfg.BlockIntervalDelta = 0
	}
	if cfg.InitialHeight <= 0 {
		cfg.InitialHeight = 1
	}
	if cfg.Start.IsZero() {
		cfg.Start = time.Unix(0, 0).UTC()
	} else {
		cfg.Start = cfg.Start.UTC()
	}

	// Strict > 3T/4 remaining <=> dropped < T/4 (integer: dropped ≤ (T-1)/4).
	// For T=1..3 this is 0 — we always sign with every validator.
	maxDropPower := (total - 1) / 4
	if maxDropPower < 0 {
		maxDropPower = 0
	}

	return &Mock{
		cfg:          cfg,
		totalPower:   total,
		maxDropPower: maxDropPower,
		history:      make(map[int64]*blocks.Header),
		subs:         make(map[int]*subscription),
	}, nil
}

// Run fabricates one header per BlockInterval tick until ctx is
// cancelled. It returns ctx.Err() when cancelled.
func (m *Mock) Run(ctx context.Context) error {
	// Emit the genesis header immediately so subscribers have something
	// to anchor on before the first tick fires.
	if _, err := m.AdvanceOne(); err != nil {
		return err
	}
	nextHeight := m.cfg.InitialHeight + 1
	for {
		wait := m.intervalForHeight(nextHeight)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			m.close()
			return ctx.Err()
		case <-timer.C:
			if _, err := m.AdvanceOne(); err != nil {
				return err
			}
			nextHeight++
		}
	}
}

// AdvanceOne produces and returns the next fabricated header. It is a
// test hook and is safe for concurrent use.
func (m *Mock) AdvanceOne() (*blocks.Header, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.advanceLocked()
}

func (m *Mock) advanceLocked() (*blocks.Header, error) {
	var height int64
	var t time.Time
	if m.latest == nil {
		height = m.cfg.InitialHeight
		t = m.cfg.Start
	} else {
		height = m.latest.Height + 1
		t = m.latest.Time.Add(m.intervalForHeight(height))
	}

	h := &blocks.Header{
		Height:             height,
		Time:               t,
		ChainID:            m.cfg.ChainID,
		BlockHash:          m.deriveHash("block", height),
		AppHash:            m.deriveHash("app", height),
		ValidatorsHash:     m.deriveHash("validators", height),
		NextValidatorsHash: m.deriveHash("next_validators", height+1),
	}
	sigs, err := m.signCommit(h, height, t)
	if err != nil {
		return nil, err
	}
	h.Commit = blocks.Commit{
		Height:     height,
		Round:      0,
		BlockID:    h.BlockHash,
		Signatures: sigs,
	}

	m.latest = h
	m.history[height] = h
	m.fanoutLocked(h)
	return h, nil
}

func (m *Mock) intervalForHeight(height int64) time.Duration {
	base := m.cfg.BlockInterval
	delta := m.cfg.BlockIntervalDelta
	if delta <= 0 {
		return base
	}

	deltaNs := delta.Nanoseconds()
	if deltaNs <= 0 {
		return base
	}

	// Deterministic jitter from (seed, height) so runs are reproducible.
	var seedBuf [16]byte
	binary.BigEndian.PutUint64(seedBuf[:8], uint64(m.cfg.Seed)^0x9e3779b97f4a7c15)
	binary.BigEndian.PutUint64(seedBuf[8:], uint64(height))
	state := sha256.Sum256(append([]byte("mock-interval-jitter:"), seedBuf[:]...))

	span := uint64(2*deltaNs + 1) // [-delta, +delta]
	j := int64(binary.BigEndian.Uint64(state[:8])%span) - deltaNs
	d := base + time.Duration(j)
	if d <= 0 {
		return time.Millisecond
	}
	return d
}

// signCommit returns a deterministic subset of the pinned validators'
// signatures over the canonical header bytes. At least one validator is
// always present, and the signing set's total power is strictly
// > 3/4 of the configured total (stricter than the verifier's > 2/3).
func (m *Mock) signCommit(h *blocks.Header, height int64, t time.Time) ([]blocks.CommitSig, error) {
	canonical := blocks.CanonicalHeaderBytes(h)
	dropped := m.pickDropSet(height)

	// Preserve the configured validator order in Commit.Signatures so
	// the canonical header bytes are deterministic across restarts.
	sigs := make([]blocks.CommitSig, 0, len(m.cfg.Validators))
	for i, v := range m.cfg.Validators {
		if _, skip := dropped[i]; skip {
			continue
		}
		sig, err := v.Signer.Sign(canonical)
		if err != nil {
			return nil, fmt.Errorf("mock observer: sign with validator[%d]: %w", i, err)
		}
		sigs = append(sigs, blocks.CommitSig{
			ValidatorAddress: append([]byte(nil), v.Address...),
			Timestamp:        t,
			Signature:        sig,
		})
	}
	if len(sigs) == 0 {
		// Defensive: pickDropSet guarantees at least one remaining, but
		// keep an explicit error so regressions are loud.
		return nil, errors.New("mock observer: no validators left to sign")
	}
	return sigs, nil
}

// pickDropSet returns a deterministic set of validator indices to skip
// for this height. The choice is a function of (seed, height) so two
// observers with the same config emit byte-identical headers.
//
// Invariant: the total power of the returned set is ≤ maxDropPower,
// i.e. remaining power is strictly > 3/4 of the configured total.
func (m *Mock) pickDropSet(height int64) map[int]struct{} {
	if m.maxDropPower == 0 || len(m.cfg.Validators) <= 1 {
		return nil
	}

	// Seed a deterministic stream from (seed, height).
	var seedBuf [16]byte
	binary.BigEndian.PutUint64(seedBuf[:8], uint64(m.cfg.Seed))
	binary.BigEndian.PutUint64(seedBuf[8:], uint64(height))
	state := sha256.Sum256(append([]byte("mock-drop:"), seedBuf[:]...))

	// Draw a power budget uniformly in [0, maxDropPower]. This yields a
	// mix of "all signatures present" (budget=0) and "trim some out"
	// (budget>0) blocks.
	budget := int64(state[0]) % (m.maxDropPower + 1)
	if budget == 0 {
		return nil
	}

	// Fisher-Yates shuffle indices using state as the randomness source,
	// rehashing the state as we go so the stream is unbounded.
	n := len(m.cfg.Validators)
	perm := make([]int, n)
	for i := range perm {
		perm[i] = i
	}
	rng := state
	for i := n - 1; i > 0; i-- {
		k := int(binary.BigEndian.Uint32(rng[:4])) % (i + 1)
		if k < 0 {
			k += i + 1
		}
		perm[i], perm[k] = perm[k], perm[i]
		rng = sha256.Sum256(rng[:])
	}

	dropped := make(map[int]struct{})
	consumed := int64(0)
	for _, idx := range perm {
		p := m.cfg.Validators[idx].Power
		if consumed+p > budget {
			continue
		}
		dropped[idx] = struct{}{}
		consumed += p
		if consumed == budget {
			break
		}
	}
	return dropped
}

// Validators returns the pinned validators in config order. Consumers
// that want to drive a verifier can build ValidatorSet from this slice.
func (m *Mock) Validators() []MockValidator {
	// Return a deterministic copy so callers cannot mutate internal state.
	out := make([]MockValidator, len(m.cfg.Validators))
	for i, v := range m.cfg.Validators {
		out[i] = MockValidator{
			Signer:  v.Signer,
			Address: append([]byte(nil), v.Address...),
			Power:   v.Power,
		}
	}
	return out
}

// TotalPower returns the sum of voting power across the pinned validators.
func (m *Mock) TotalPower() int64 { return m.totalPower }

// deriveHash is deterministic in (tag, seed, height).
func (m *Mock) deriveHash(tag string, height int64) []byte {
	var hb [8]byte
	binary.BigEndian.PutUint64(hb[:], uint64(height))
	buf := make([]byte, 0, len(tag)+32+8)
	buf = append(buf, tag...)
	buf = append(buf, ':')
	buf = append(buf, strconv.FormatInt(m.cfg.Seed, 10)...)
	buf = append(buf, ':')
	buf = append(buf, hb[:]...)
	sum := sha256.Sum256(buf)
	return sum[:]
}

// Latest returns the most recent fabricated header or an error if the
// observer hasn't produced any yet.
func (m *Mock) Latest(_ context.Context) (*blocks.Header, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.latest == nil {
		return nil, errors.New("mock observer: no headers produced yet")
	}
	return cloneHeader(m.latest), nil
}

// At returns the header at a specific height. Out-of-range heights
// return an error.
func (m *Mock) At(_ context.Context, height int64) (*blocks.Header, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	h, ok := m.history[height]
	if !ok {
		return nil, fmt.Errorf("mock observer: no header at height %d", height)
	}
	return cloneHeader(h), nil
}

// Prove returns a minimal, deterministic proof for testenv scenarios.
// The proof is not an IAVL proof; it binds (path, height) to the
// header's AppHash so consumers can exercise the wire format without
// requiring a real state tree.
func (m *Mock) Prove(_ context.Context, path string, height int64) (*blocks.Proof, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	h, ok := m.history[height]
	if !ok {
		return nil, fmt.Errorf("mock observer: no header at height %d for proof", height)
	}
	var hb [8]byte
	binary.BigEndian.PutUint64(hb[:], uint64(height))
	op := sha256.Sum256(append([]byte(path+":"), hb[:]...))
	return &blocks.Proof{
		Path:  path,
		Value: append([]byte(nil), h.AppHash...),
		Ops:   [][]byte{append([]byte(nil), op[:]...)},
	}, nil
}

// Subscribe returns a channel that first replays any headers at or after
// fromHeight, then receives every new header until ctx is cancelled.
// The channel is closed when the subscription ends.
func (m *Mock) Subscribe(ctx context.Context, fromHeight int64) (<-chan *blocks.Header, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("mock observer: closed")
	}
	sub := &subscription{
		ch:   make(chan *blocks.Header, 16),
		from: fromHeight,
	}
	id := m.nextSub
	m.nextSub++
	m.subs[id] = sub

	// Replay history in ascending order without blocking under lock.
	replay := make([]*blocks.Header, 0)
	if m.latest != nil {
		lo := fromHeight
		if lo < m.cfg.InitialHeight {
			lo = m.cfg.InitialHeight
		}
		for h := lo; h <= m.latest.Height; h++ {
			if v, ok := m.history[h]; ok {
				replay = append(replay, cloneHeader(v))
			}
		}
	}
	m.mu.Unlock()

	// Deliver replay and wire the teardown goroutine.
	go func() {
		for _, h := range replay {
			select {
			case <-ctx.Done():
				m.unsubscribe(id)
				return
			case sub.ch <- h:
			}
		}
		<-ctx.Done()
		m.unsubscribe(id)
	}()
	return sub.ch, nil
}

func (m *Mock) unsubscribe(id int) {
	m.mu.Lock()
	sub, ok := m.subs[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	delete(m.subs, id)
	m.mu.Unlock()
	close(sub.ch)
}

func (m *Mock) fanoutLocked(h *blocks.Header) {
	for id, sub := range m.subs {
		if h.Height < sub.from {
			continue
		}
		cp := cloneHeader(h)
		select {
		case sub.ch <- cp:
		default:
			// Slow consumer: drop the subscription rather than blocking the
			// producer. Tests assert fan-out works; production uses the
			// HTTP SSE bridge which has its own buffering.
			delete(m.subs, id)
			close(sub.ch)
		}
	}
}

func (m *Mock) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	for id, sub := range m.subs {
		delete(m.subs, id)
		close(sub.ch)
	}
}

func cloneHeader(h *blocks.Header) *blocks.Header {
	if h == nil {
		return nil
	}
	cp := *h
	cp.BlockHash = append([]byte(nil), h.BlockHash...)
	cp.AppHash = append([]byte(nil), h.AppHash...)
	cp.ValidatorsHash = append([]byte(nil), h.ValidatorsHash...)
	cp.NextValidatorsHash = append([]byte(nil), h.NextValidatorsHash...)
	cp.Commit = blocks.Commit{
		Height:     h.Commit.Height,
		Round:      h.Commit.Round,
		BlockID:    append([]byte(nil), h.Commit.BlockID...),
		Signatures: make([]blocks.CommitSig, len(h.Commit.Signatures)),
	}
	for i, s := range h.Commit.Signatures {
		cp.Commit.Signatures[i] = blocks.CommitSig{
			ValidatorAddress: append([]byte(nil), s.ValidatorAddress...),
			Timestamp:        s.Timestamp,
			Signature:        append([]byte(nil), s.Signature...),
		}
	}
	return &cp
}

// Compile-time assertion that Mock implements Observer (and thus
// blocks.BlockOracle).
var _ Observer = (*Mock)(nil)
