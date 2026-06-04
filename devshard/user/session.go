package user

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"devshard"
	"devshard/host"
	"devshard/logging"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/types"
)

// TimeoutBuffer is added to protocol deadlines so verifiers have
// passed their own deadline before the proxy fires the timeout.
var TimeoutBuffer = 5 * time.Second

// MaxConcurrentVerifierRPCs caps how many simultaneous VerifyTimeout RPCs the
// proxy may have open against the same verifier host. When many in-flight
// nonces time out around the same time (e.g. one executor host stops
// responding), CollectTimeoutVotes fans out one VerifyTimeout per verifier
// per timeout. Without this cap, M concurrent timeouts × N verifiers can
// exhaust the per-host connection budget on every verifier in the group.
//
// The limit is per verifier (keyed by validator address) and is enforced
// process-wide via SharedVerifierQueue so that different Sessions (one per
// escrow / devshard runtime) can't collectively pile connections onto the same
// verifier host. Different verifiers are still hit in parallel; only
// per-verifier RPCs serialize.
var MaxConcurrentVerifierRPCs = 1

// VerifierQueueWaitTimeout bounds how long a VerifyTimeout goroutine may wait
// for its verifier-host slot before giving up. When a verifier hangs, its
// in-flight RPC can occupy the slot up to the transport-level VerifyTimeout
// deadline (default 3m). Without a wait cap, every new timeout for that
// verifier would queue indefinitely, leaking goroutines and producing stale
// votes if we ever did dequeue. This deadline:
//   - bounds goroutine count under persistent verifier failure
//     (depth ≤ request_rate × VerifierQueueWaitTimeout),
//   - and guarantees that nothing more than VerifierQueueWaitTimeout old
//     will ever produce a VerifyTimeout RPC.
//
// An expired wait is reported as a verifier error and counted in the
// CollectTimeoutVotes `errors` tally — the same way a transport-level
// failure is reported — so the vote collection continues with the
// remaining verifiers.
var VerifierQueueWaitTimeout = 120 * time.Second

// verifierHostQueue serializes outbound VerifyTimeout RPCs per verifier host.
// Each verifier (keyed by validator address) gets a buffered channel acting
// as a semaphore of capacity MaxConcurrentVerifierRPCs. Acquire is
// ctx-aware so a cancelled timeout collection does not stay queued
// forever.
type verifierHostQueue struct {
	mu    sync.Mutex
	slots map[string]chan struct{}
}

func newVerifierHostQueue() *verifierHostQueue {
	return &verifierHostQueue{slots: make(map[string]chan struct{})}
}

// SharedVerifierQueue is the process-wide verifier-host limiter. All Sessions
// created with NewSession share it by default, so one proxy runtime cannot
// open more than MaxConcurrentVerifierRPCs concurrent VerifyTimeout RPCs to a
// single verifier across all of its escrows. Tests may inject a private
// queue via WithVerifierQueue to keep assertions isolated.
var SharedVerifierQueue = newVerifierHostQueue()

func (q *verifierHostQueue) slot(addr string) chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()
	sem, ok := q.slots[addr]
	if !ok {
		capacity := MaxConcurrentVerifierRPCs
		if capacity < 1 {
			capacity = 1
		}
		sem = make(chan struct{}, capacity)
		q.slots[addr] = sem
	}
	return sem
}

// acquire blocks until a slot is available for addr or ctx is cancelled.
// Returns ctx.Err() if cancelled while waiting.
func (q *verifierHostQueue) acquire(ctx context.Context, addr string) error {
	sem := q.slot(addr)
	select {
	case sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// release returns one slot to addr's semaphore. Must be called exactly once
// after a successful acquire.
func (q *verifierHostQueue) release(addr string) {
	sem := q.slot(addr)
	<-sem
}

// nonceOutcome tracks protocol-relevant facts observed for a single inference nonce.
type nonceOutcome struct {
	confirmedAt int64
	finished    bool
}

// TimeoutResult reports what happened during timeout handling.
type TimeoutResult struct {
	Reason string // "execution", "refused", or "" if deadline not reached
}

// HasMsgFinish returns true if mempool contains MsgFinishInference for the given nonce.
func HasMsgFinish(txs []*types.DevshardTx, nonce uint64) bool {
	for _, tx := range txs {
		if fi := tx.GetFinishInference(); fi != nil && fi.InferenceId == nonce {
			return true
		}
	}
	return false
}

type HostClient interface {
	Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func()) (*host.HostResponse, error)
}

// SignatureFetcher is optionally implemented by HostClient implementations that
// can retrieve stored signatures without sending diffs. Used by Finalize Phase B
// to avoid redundant diff processing when the host already signed via gossip.
type SignatureFetcher interface {
	GetSignatures(ctx context.Context, nonce uint64) (map[uint32][]byte, error)
}

type InProcessClient struct {
	Host *host.Host
}

// writeInProcessStreamingChunk emits a minimal but valid SSE streaming
// response body — role marker, one delta content chunk, [DONE] — so that a
// proxy's race writer sees a content-bearing `delta.content` event. This is
// the shape a real vLLM backend produces for stream=true; the in-process
// client uses it instead of wrapping the canonical non-streaming body so we
// don't accidentally mirror the exact attack pattern the proxy's content
// detector is designed to reject.
func writeInProcessStreamingChunk(stream io.Writer) {
	fmt.Fprintf(stream, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")
	fmt.Fprintf(stream, "data: {\"choices\":[{\"delta\":{\"content\":\"stub\"}}]}\n\n")
	fmt.Fprintf(stream, "data: [DONE]\n\n")
}

func (c *InProcessClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func()) (*host.HostResponse, error) {
	resp, err := c.Host.HandleRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	// Honor the HostClient contract: real HTTP clients invoke receiptHandler
	// once the response headers come back. The in-process equivalent is "as
	// soon as HandleRequest returns successfully". Without this, the proxy's
	// race writer never sees a receipt and stall-detection logic cannot tell
	// the in-process path apart from a real-world stall.
	if receiptHandler != nil {
		receiptHandler()
	}
	if resp.ExecutionJob != nil {
		result, execErr := c.Host.RunExecution(ctx, resp.ExecutionJob)
		if execErr != nil {
			logging.Error("deferred execution failed", "subsystem", "in_process_client", "error", execErr)
		} else if stream != nil && result != nil && len(result.ResponseBody) > 0 {
			// Emit a proper streaming SSE chunk so the proxy's race writer
			// counts it as a content-bearing `delta` event, matching what a
			// real vLLM backend emits when stream=true. We deliberately do
			// NOT wrap the canonical non-streaming ResponseBody (which uses
			// the `message` shape and is kept intact for on-chain hashing)
			// — that shape is unrenderable by streaming clients and would
			// be correctly rejected by the proxy's content detector.
			writeInProcessStreamingChunk(stream)
		}
		// Re-fetch mempool after execution.
		resp.Mempool = c.Host.MempoolTxs()
	} else if stream != nil && len(resp.CachedResponseBody) > 0 {
		// Reconnect path: same rationale as above — emit a streaming shape,
		// not the cached non-streaming body.
		writeInProcessStreamingChunk(stream)
	}
	return resp, nil
}

// InferenceParams describes a new inference to send.
type InferenceParams struct {
	Model            string
	Prompt           []byte
	InputLength      uint64
	MaxTokens        uint64
	ContextTotalHint uint64
	StartedAt        int64
	Stream           bool
}

// Session manages the user side of the devshard protocol.
type Session struct {
	mu          sync.Mutex
	sm          *state.StateMachine
	signer      signing.Signer
	verifier    signing.Verifier
	escrowID    string
	group       []types.SlotAssignment
	addrToSlots map[string][]uint32 // validator address -> slot IDs
	// participantKeys[i] is the canonical participant identifier for
	// slot i: always the slot's gonka validator address (bech32). This
	// matches the keying used by chain-side state in
	// CapacityState/ParticipantRequestLimiter and by the transport
	// admission controller. Multi-slot validators legitimately repeat
	// the same key here; ParticipantKeys() de-duplicates for views
	// that want a per-host (not per-slot) list.
	participantKeys []string
	clients         []HostClient
	nonce           uint64
	diffs           []types.Diff                 // append-only log
	hostSyncNonce   map[int]uint64               // hostIdx -> last nonce sent
	pendingTxs      []*types.DevshardTx          // from host mempools, for next diff
	pendingTxKeys   map[string]struct{}          // dedup set keyed by tx_type:id
	signatures      map[uint64]map[uint32][]byte // nonce -> slotID -> sig
	store           storage.Storage              // optional persistent storage
	nonceStates     map[uint64]*nonceOutcome     // nonce -> protocol outcome
	verifierQueue   *verifierHostQueue           // per-verifier RPC limiter for timeout votes

	// snapshotInFlight is set to true while an async background snapshot
	// save is running, so concurrent composeDiffLocked invocations do not
	// pile up duplicate saves. See maybeSaveSnapshotLocked.
	snapshotInFlight atomic.Bool

	// finalizeInFlight guards against concurrent Finalize calls. A second
	// call while the first is still running returns immediately with an error
	// instead of spawning duplicate catch-up goroutines to the same hosts.
	finalizeInFlight atomic.Bool

	// Retry settings for signature collection (handles transient host failures during finalize).
	signatureCollectMaxRetries  int
	signatureCollectBaseDelay   time.Duration
	signatureCollectHostTimeout time.Duration

	// finalizeClients mirrors s.clients with admission control stripped.
	// Built lazily on first CollectSignatures call so finalize catch-up
	// can reach quarantined hosts.
	finalizeClients []HostClient
}

// SessionOption configures optional Session behavior.
type SessionOption func(*Session)

// WithStorage sets a persistent storage backend for the session.
// When set, diffs and signatures are persisted on each state transition.
func WithStorage(s storage.Storage) SessionOption {
	return func(sess *Session) { sess.store = s }
}

// WithCollectRetry overrides signature collection retry parameters.
func WithCollectRetry(maxRetries int, baseDelay, hostTimeout time.Duration) SessionOption {
	return func(sess *Session) {
		sess.signatureCollectMaxRetries = maxRetries
		sess.signatureCollectBaseDelay = baseDelay
		sess.signatureCollectHostTimeout = hostTimeout
	}
}

// WithVerifierQueue overrides the per-verifier RPC limiter. The default is
// SharedVerifierQueue (process-wide). Tests can pass a private queue to
// assert on concurrency without being affected by concurrent runs.
func WithVerifierQueue(q *verifierHostQueue) SessionOption {
	return func(sess *Session) {
		if q != nil {
			sess.verifierQueue = q
		}
	}
}

// NewSession creates a user session. clients must match group length.
func NewSession(
	sm *state.StateMachine,
	signer signing.Signer,
	escrowID string,
	group []types.SlotAssignment,
	clients []HostClient,
	verifier signing.Verifier,
	opts ...SessionOption,
) (*Session, error) {
	if err := types.ValidateGroup(group); err != nil {
		return nil, err
	}
	if len(clients) != len(group) {
		return nil, fmt.Errorf("%w: got %d clients for %d slots",
			types.ErrGroupSizeMismatch, len(clients), len(group))
	}
	addrToSlots := make(map[string][]uint32, len(group))
	for _, s := range group {
		addrToSlots[s.ValidatorAddress] = append(addrToSlots[s.ValidatorAddress], s.SlotID)
	}
	sess := &Session{
		sm:              sm,
		signer:          signer,
		verifier:        verifier,
		escrowID:        escrowID,
		group:           group,
		addrToSlots:     addrToSlots,
		participantKeys: make([]string, len(group)),
		clients:         clients,
		hostSyncNonce:   make(map[int]uint64),
		pendingTxKeys:   make(map[string]struct{}),
		signatures:      make(map[uint64]map[uint32][]byte),
		nonceStates:     make(map[uint64]*nonceOutcome),
		verifierQueue:   SharedVerifierQueue,
		//TODO: check if we should move it from Session
		signatureCollectMaxRetries:  3,
		signatureCollectBaseDelay:   2 * time.Second,
		signatureCollectHostTimeout: 30 * time.Second,
	}
	for i, slot := range group {
		sess.participantKeys[i] = slot.ValidatorAddress
	}
	for _, opt := range opts {
		opt(sess)
	}
	return sess, nil
}

// txPriority returns a sort key for pending tx ordering.
// The state machine requires ConfirmStart before FinishInference before Validation.
func txPriority(tx *types.DevshardTx) int {
	switch tx.GetTx().(type) {
	case *types.DevshardTx_ConfirmStart:
		return 0
	case *types.DevshardTx_FinishInference:
		return 1
	case *types.DevshardTx_Validation:
		return 2
	case *types.DevshardTx_ValidationVote:
		return 3
	default:
		return 4
	}
}

// diffsForHost returns catch-up diffs for a host (from its last sync nonce to current).
// Caller must hold s.mu.
func (s *Session) diffsForHost(hostIdx int) []types.Diff {
	lastSent := s.hostSyncNonce[hostIdx]
	var result []types.Diff
	for _, d := range s.diffs {
		if d.Nonce > lastSent {
			result = append(result, d)
		}
	}
	return result
}

// validateCatchUp warns if the catch-up diffs for a host are non-contiguous
// or miss the target nonce. Either condition means the host will fail to apply
// the diff chain and signReceipt will return nil. This is a diagnostic-only
// check; it never mutates state.
// Caller must hold s.mu.
func (s *Session) validateCatchUp(diffs []types.Diff, targetNonce uint64, hostIdx int) {
	if len(diffs) == 0 {
		logging.Error("catch_up_empty",
			"subsystem", "session",
			"escrow", s.escrowID,
			"host_idx", hostIdx,
			"target_nonce", targetNonce,
			"session_nonce", s.nonce,
			"host_sync_nonce", s.hostSyncNonce[hostIdx],
			"diffs_len", len(s.diffs),
		)
		return
	}

	found := false
	for i, d := range diffs {
		if d.Nonce == targetNonce {
			found = true
		}
		if i > 0 && d.Nonce != diffs[i-1].Nonce+1 {
			logging.Error("catch_up_gap",
				"subsystem", "session",
				"escrow", s.escrowID,
				"host_idx", hostIdx,
				"target_nonce", targetNonce,
				"gap_after", diffs[i-1].Nonce,
				"next_nonce", d.Nonce,
				"host_sync_nonce", s.hostSyncNonce[hostIdx],
				"diffs_total", len(s.diffs),
				"catch_up_len", len(diffs),
			)
		}
	}

	if !found {
		logging.Error("catch_up_missing_target",
			"subsystem", "session",
			"escrow", s.escrowID,
			"host_idx", hostIdx,
			"target_nonce", targetNonce,
			"catch_up_first", diffs[0].Nonce,
			"catch_up_last", diffs[len(diffs)-1].Nonce,
			"host_sync_nonce", s.hostSyncNonce[hostIdx],
			"diffs_total", len(s.diffs),
		)
	}
}

// postStateRootForNonce returns the persisted post-state root for the given
// nonce when it is present in s.diffs. Recovery may intentionally keep only a
// contiguous suffix of diffs (for stranded-host catch-up), so callers must not
// assume s.diffs is indexed from nonce 1.
func (s *Session) postStateRootForNonce(nonce uint64) ([]byte, bool) {
	if len(s.diffs) == 0 {
		return nil, false
	}
	firstNonce := s.diffs[0].Nonce
	if nonce >= firstNonce {
		idx := nonce - firstNonce
		if idx < uint64(len(s.diffs)) {
			diff := s.diffs[idx]
			if diff.Nonce == nonce {
				return diff.PostStateRoot, true
			}
		}
	}
	for _, diff := range s.diffs {
		if diff.Nonce == nonce {
			return diff.PostStateRoot, true
		}
	}
	return nil, false
}

// processResponse updates session state from a host response.
// inferenceNonce is the nonce assigned during PrepareInference (the logical inference ID).
// resp.Nonce may differ when the host has already advanced past inferenceNonce.
// Caller must hold s.mu.
func (s *Session) processResponse(hostIdx int, resp *host.HostResponse, inferenceNonce uint64) error {
	// Verify state hash if the host returned one.
	if len(resp.StateHash) > 0 {
		var expected []byte
		if root, ok := s.postStateRootForNonce(resp.Nonce); ok {
			expected = root
		} else {
			// Finalize path: nonce beyond diffs array, compute live.
			var err error
			expected, err = s.sm.ComputeStateRoot()
			if err != nil {
				return fmt.Errorf("compute local state root: %w", err)
			}
		}
		if !bytes.Equal(expected, resp.StateHash) {
			return fmt.Errorf("%w: host %d at nonce %d (local %x, host %x)",
				types.ErrStateHashMismatch, hostIdx, resp.Nonce, expected, resp.StateHash)
		}
	}

	// Verify and store state signature.
	if resp.StateSig != nil {
		expectedAddr := s.group[hostIdx].ValidatorAddress
		sigContent := &types.StateSignatureContent{
			StateRoot: resp.StateHash,
			EscrowId:  s.escrowID,
			Nonce:     resp.Nonce,
		}
		sigData, err := proto.Marshal(sigContent)
		if err != nil {
			return fmt.Errorf("marshal state sig content: %w", err)
		}
		addr, err := s.verifier.RecoverAddress(sigData, resp.StateSig)
		if err != nil {
			return fmt.Errorf("%w: host %d: %v", types.ErrInvalidStateSig, hostIdx, err)
		}
		if addr != expectedAddr {
			if !s.sm.CheckWarmKey(addr, expectedAddr) {
				return fmt.Errorf("%w: host %d: expected %s, got %s",
					types.ErrInvalidStateSig, hostIdx, expectedAddr, addr)
			}
		}

		// Store for all slots owned by this validator address.
		if _, ok := s.signatures[resp.Nonce]; !ok {
			s.signatures[resp.Nonce] = make(map[uint32][]byte)
		}
		for _, slot := range s.addrToSlots[expectedAddr] {
			s.signatures[resp.Nonce][slot] = resp.StateSig
			if s.store != nil {
				if sigErr := s.store.AddSignature(s.escrowID, resp.Nonce, slot, resp.StateSig); sigErr != nil {
					logging.Warn("failed to persist signature",
						"escrow_id", s.escrowID, "nonce", resp.Nonce, "slot", slot, "error", sigErr)
				}
			}
		}
	}

	// Update sync nonce -- only advance, never regress.
	if resp.Nonce > s.hostSyncNonce[hostIdx] {
		s.hostSyncNonce[hostIdx] = resp.Nonce
	}

	// Queue receipt as MsgConfirmStart for the next diff.
	// Use inferenceNonce (the logical inference ID), not resp.Nonce (host's latest state).
	if resp.Receipt != nil {
		s.addPendingTx(&types.DevshardTx{
			Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
				InferenceId: inferenceNonce,
				ExecutorSig: resp.Receipt,
				ConfirmedAt: resp.ConfirmedAt,
			}},
		})
	}

	// Queue mempool txs (finish msgs) for the next diff.
	for _, tx := range resp.Mempool {
		s.addPendingTx(tx)
	}

	// Track protocol outcome for this nonce (only for prepared inferences).
	if outcome, ok := s.nonceStates[inferenceNonce]; ok {
		if resp.ConfirmedAt > 0 {
			outcome.confirmedAt = resp.ConfirmedAt
		}
		if HasMsgFinish(resp.Mempool, inferenceNonce) {
			outcome.finished = true
		}
	}

	return nil
}

// ProcessResponse updates session state from a host response. Thread-safe.
func (s *Session) ProcessResponse(hostIdx int, resp *host.HostResponse, inferenceNonce uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.processResponse(hostIdx, resp, inferenceNonce)
}

// PreparedInference holds the data prepared under lock for an inference send.
type PreparedInference struct {
	diff    types.Diff
	hostIdx int
	catchUp []types.Diff
	params  InferenceParams
	isProbe bool
}

// HostBinding describes the host slot that the next nonce will be
// dispatched to. It is supplied to ParamsForHost so the chooser can make
// its decision (probe vs real, allowed vs excluded, etc.) without calling
// back into Session -- the chooser runs under Session.mu, so any Session
// method that takes the same lock would deadlock.
type HostBinding struct {
	HostIdx        int
	Nonce          uint64
	ParticipantKey string
	ValidatorAddr  string
}

// ParamsForHost returns the params (and probe flag) to use for a freshly
// allocated nonce, given the host that will receive the diff. It is
// invoked under Session.mu while the nonce is being assigned, so the
// HostBinding the chooser sees is the same host the resulting
// PreparedInference will dispatch to. This avoids the racy "peek nonce,
// decide, then call PrepareInference" pattern where the host could change
// between peek and commit.
//
// If the chooser returns a non-nil error, PrepareInferenceFn aborts
// without consuming the nonce and propagates the error to the caller.
// This lets policy callers (queue picker, exclude lists) decline a
// binding that is no longer useful (e.g. the queued request was canceled
// in the gap between wakeup and lock acquisition).
type ParamsForHost func(b HostBinding) (params InferenceParams, probe bool, err error)

// composeDiffLocked builds, applies, persists, and returns a new diff.
// extraTxs are prepended to pending txs. Caller must hold s.mu.
func (s *Session) composeDiffLocked(extraTxs []*types.DevshardTx) (types.Diff, int, error) {
	nonce := s.nonce + 1
	hostIdx := int(nonce % uint64(len(s.group)))

	sort.SliceStable(s.pendingTxs, func(i, j int) bool {
		return txPriority(s.pendingTxs[i]) < txPriority(s.pendingTxs[j])
	})

	candidates := make([]*types.DevshardTx, 0, len(s.pendingTxs)+len(extraTxs))
	candidates = append(candidates, s.pendingTxs...)
	candidates = append(candidates, extraTxs...)

	var warmBefore map[uint32]string
	if s.store != nil {
		warmBefore = s.sm.WarmKeys()
	}
	postStateRoot, applied, err := s.sm.ApplyLocalBestEffort(nonce, candidates)
	if err != nil {
		return types.Diff{}, 0, fmt.Errorf("local apply: %w", err)
	}
	diff, err := s.signDiff(nonce, applied, postStateRoot)
	if err != nil {
		return types.Diff{}, 0, err
	}

	s.diffs = append(s.diffs, diff)
	s.nonce = nonce
	s.clearPendingTxs()

	if s.store != nil {
		warmAfter := s.sm.WarmKeys()
		delta := types.ComputeWarmKeyDelta(warmBefore, warmAfter)
		if err := s.store.AppendDiff(s.escrowID, types.DiffRecord{
			Diff:         diff,
			StateHash:    postStateRoot,
			WarmKeyDelta: delta,
		}); err != nil {
			return types.Diff{}, 0, fmt.Errorf("persist diff: %w", err)
		}
		s.maybeSaveSnapshotLocked()
	}

	return diff, hostIdx, nil
}

// maybeSaveSnapshotLocked schedules an asynchronous snapshot save when
// the current nonce is on the snapshot interval. The deep copy of state
// and per-host cursor is taken under the existing s.mu lock so the
// snapshot is consistent; the JSON marshal and storage write run on a
// goroutine without any session locks held.
//
// snapshotInFlight (atomic CAS) ensures that if a previous save hasn't
// finished by the next interval boundary we skip rather than pile up
// concurrent writers. Skipping is safe -- the cursor will simply be
// captured at the next interval (snapshotInterval nonces later).
//
// Caller must hold s.mu.
func (s *Session) maybeSaveSnapshotLocked() {
	if s.store == nil {
		return
	}
	if s.nonce == 0 || s.nonce%snapshotInterval != 0 {
		return
	}
	if !s.snapshotInFlight.CompareAndSwap(false, true) {
		return
	}

	// Deep-copy state and cursor under the session lock; release before
	// any disk IO. ExportState takes its own SM RLock for the deep copy.
	state := s.sm.ExportState()
	cursor := make(map[int]uint64, len(s.hostSyncNonce))
	for k, v := range s.hostSyncNonce {
		cursor[k] = v
	}
	nonce := s.nonce
	store := s.store
	escrowID := s.escrowID

	go func() {
		defer s.snapshotInFlight.Store(false)
		writeSnapshot(store, escrowID, nonce, state, cursor)
	}()
}

// PrepareInference composes a diff, applies it locally, advances nonce,
// and returns everything needed for the HTTP send. Thread-safe.
//
// This is a convenience wrapper around PrepareInferenceFn for callers that
// do not need to vary the request payload based on the actually-allocated
// host. New callers that DO need a host-dependent decision (e.g. PoC probe
// shaping, exclude-list handling) should use PrepareInferenceFn so the
// decision and the nonce increment happen in the same critical section.
func (s *Session) PrepareInference(params InferenceParams) (*PreparedInference, error) {
	return s.PrepareInferenceFn(func(HostBinding) (InferenceParams, bool, error) { return params, false, nil })
}

// PrepareInferenceFn is the atomic form of PrepareInference. The chooser is
// invoked under s.mu after the nonce-to-host mapping is determined, so the
// hostIdx it sees is the host the resulting PreparedInference will dispatch
// to. This eliminates the previous race where prepareInflight peeked the
// next host with Session.Nonce(), decided whether to send a probe, then
// called PrepareInference in a separate critical section -- a concurrent
// inference could grab the peeked nonce in between, leaving the probe
// decision applied to the wrong host.
func (s *Session) PrepareInferenceFn(chooser ParamsForHost) (*PreparedInference, error) {
	if chooser == nil {
		return nil, fmt.Errorf("PrepareInferenceFn: chooser is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	nonce := s.nonce + 1
	hostIdx := int(nonce % uint64(len(s.group)))
	binding := HostBinding{
		HostIdx:        hostIdx,
		Nonce:          nonce,
		ParticipantKey: s.hostParticipantKeyLocked(hostIdx),
		ValidatorAddr:  strings.TrimSpace(s.group[hostIdx].ValidatorAddress),
	}
	params, probe, err := chooser(binding)
	if err != nil {
		// Chooser declined this binding (e.g. queue empty after dropping
		// canceled requests). Bail without consuming the nonce so the next
		// caller of PrepareInferenceFn observes the same nonce.
		return nil, err
	}

	promptHash, err := devshard.CanonicalPromptHash(params.Prompt)
	if err != nil {
		return nil, fmt.Errorf("canonical prompt hash: %w", err)
	}
	startTx := &types.DevshardTx{Tx: &types.DevshardTx_StartInference{
		StartInference: &types.MsgStartInference{
			InferenceId: nonce,
			Model:       params.Model,
			PromptHash:  promptHash,
			InputLength: params.InputLength,
			MaxTokens:   params.MaxTokens,
			StartedAt:   params.StartedAt,
		},
	}}

	diff, composedIdx, err := s.composeDiffLocked([]*types.DevshardTx{startTx})
	if err != nil {
		return nil, err
	}
	if composedIdx != hostIdx {
		// composeDiffLocked derives hostIdx from nonce % len(group) the same
		// way we do above; a mismatch would mean the lock didn't actually
		// serialise the increment with our chooser, which would resurrect
		// the very race this method exists to prevent.
		return nil, fmt.Errorf("internal: hostIdx race detected (chooser=%d composed=%d)", hostIdx, composedIdx)
	}

	s.nonceStates[nonce] = &nonceOutcome{}

	catchUp := s.diffsForHost(hostIdx)
	// TODO: remove this when we are sure that there is no bug in CatchUp
	s.validateCatchUp(catchUp, nonce, hostIdx)
	return &PreparedInference{
		diff:    diff,
		hostIdx: hostIdx,
		catchUp: catchUp,
		params:  params,
		isProbe: probe,
	}, nil
}

// Nonce returns the nonce assigned to this prepared inference.
func (p *PreparedInference) Nonce() uint64 { return p.diff.Nonce }

// HostIdx returns the host index this inference targets.
func (p *PreparedInference) HostIdx() int { return p.hostIdx }

// IsProbe reports whether the chooser passed to PrepareInferenceFn marked
// this dispatch as a probe (e.g. PoC bypass for non-preserved hosts).
func (p *PreparedInference) IsProbe() bool { return p.isProbe }

// SendOnly sends a prepared inference to the host and returns the raw response
// without processing it. Use ProcessResponse separately to apply the response
// to session state. This split allows parallel network I/O with ordered processing.
func (s *Session) SendOnly(ctx context.Context, p *PreparedInference, stream io.Writer, receiptHandler func()) (*host.HostResponse, error) {
	return s.clients[p.hostIdx].Send(ctx, host.HostRequest{
		Diffs: p.catchUp,
		Nonce: p.diff.Nonce,
		Payload: &host.InferencePayload{
			Prompt:      p.params.Prompt,
			Model:       p.params.Model,
			InputLength: p.params.InputLength,
			MaxTokens:   p.params.MaxTokens,
			StartedAt:   p.params.StartedAt,
		},
	}, stream, receiptHandler)
}

// SendInference composes diff, sends to correct host, processes response.
func (s *Session) SendInference(ctx context.Context, params InferenceParams) (*host.HostResponse, error) {
	p, err := s.PrepareInference(params)
	if err != nil {
		return nil, err
	}
	resp, err := s.SendOnly(ctx, p, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("send to host %d: %w", p.hostIdx, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.processResponse(p.hostIdx, resp, p.diff.Nonce); err != nil {
		return nil, fmt.Errorf("process response from host %d: %w", p.hostIdx, err)
	}
	return resp, nil
}

// sendDiffRound composes a diff, sends it to the next host, processes the response.
// Returns non-nil only on compose or processResponse errors; dead hosts are silently skipped.
func (s *Session) sendDiffRound(ctx context.Context, extraTxs []*types.DevshardTx) error {
	s.mu.Lock()
	diff, hostIdx, err := s.composeDiffLocked(extraTxs)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	catchUp := s.diffsForHost(hostIdx)
	s.mu.Unlock()

	logging.Info("sendDiffRound sending", "subsystem", "finalize", "escrow", s.escrowID,
		"nonce", diff.Nonce, "host", hostIdx, "catchup_count", len(catchUp))

	resp, err := s.clients[hostIdx].Send(ctx, host.HostRequest{Diffs: catchUp, Nonce: diff.Nonce}, nil, nil)
	if err != nil {
		logging.Warn("sendDiffRound host dead", "subsystem", "finalize", "escrow", s.escrowID,
			"nonce", diff.Nonce, "host", hostIdx, "error", err)
		return nil // dead host, not fatal
	}

	logging.Info("sendDiffRound response", "subsystem", "finalize", "escrow", s.escrowID,
		"nonce", diff.Nonce, "host", hostIdx,
		"resp_nonce", resp.Nonce, "has_sig", resp.StateSig != nil)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.processResponse(hostIdx, resp, diff.Nonce); err != nil {
		return err
	}
	s.logSignatureProgress(resp.Nonce)
	return nil
}

// catchUpChunkSize is the maximum number of diffs sent in a single catch-up
// request. Large sessions can accumulate hundreds of diffs; sending them all
// at once risks timeouts and oversized request bodies. Chunking lets the host
// replay state incrementally and the proxy bail out early if any chunk fails.
const catchUpChunkSize = 200

// catchUpChunkTimeout is the per-chunk timeout for sendCatchUp. Each chunk
// of 200 diffs gets its own deadline so large catch-ups (thousands of diffs)
// don't hit a single overall timeout.
const catchUpChunkTimeout = 60 * time.Second

// sendCatchUp sends existing diffs to a host using the session's default clients.
func (s *Session) sendCatchUp(ctx context.Context, hostIdx int) error {
	return s.sendCatchUpWith(ctx, hostIdx, s.clients[hostIdx])
}

// sendCatchUpWith sends existing diffs to a host using the provided client.
// Diffs are sent in chunks of catchUpChunkSize with a per-chunk timeout.
// If any chunk fails (host dead or processResponse error), we stop --
// there's no point sending later chunks if the host couldn't apply earlier ones.
// Returns non-nil only on processResponse errors; dead hosts are silently skipped.
func (s *Session) sendCatchUpWith(ctx context.Context, hostIdx int, client HostClient) error {
	s.mu.Lock()
	nonce := s.nonce
	catchUp := s.diffsForHost(hostIdx)
	s.mu.Unlock()

	if len(catchUp) == 0 {
		return nil
	}

	totalChunks := (len(catchUp) + catchUpChunkSize - 1) / catchUpChunkSize
	logging.Info("sendCatchUp starting", "subsystem", "finalize", "escrow", s.escrowID,
		"nonce", nonce, "host", hostIdx,
		"total_diffs", len(catchUp), "chunks", totalChunks)

	chunkIdx := 0
	for chunkIdx < len(catchUp) {
		if err := ctx.Err(); err != nil {
			logging.Warn("sendCatchUp context cancelled", "subsystem", "finalize", "escrow", s.escrowID,
				"nonce", nonce, "host", hostIdx,
				"chunk", chunkIdx/catchUpChunkSize+1, "error", err)
			return nil
		}

		end := chunkIdx + catchUpChunkSize
		if end > len(catchUp) {
			end = len(catchUp)
		}
		chunk := catchUp[chunkIdx:end]
		chunkNonce := chunk[len(chunk)-1].Nonce
		chunkNum := chunkIdx/catchUpChunkSize + 1

		logging.Info("sendCatchUp chunk", "subsystem", "finalize", "escrow", s.escrowID,
			"nonce", nonce, "host", hostIdx,
			"chunk", chunkNum, "of", totalChunks,
			"diffs_in_chunk", len(chunk),
			"chunk_first_nonce", chunk[0].Nonce,
			"chunk_last_nonce", chunkNonce)

		chunkCtx, cancel := context.WithTimeout(ctx, catchUpChunkTimeout)
		resp, err := client.Send(chunkCtx, host.HostRequest{Diffs: chunk, Nonce: chunkNonce}, nil, nil)
		cancel()
		if err != nil {
			logging.Warn("sendCatchUp host dead", "subsystem", "finalize", "escrow", s.escrowID,
				"nonce", nonce, "host", hostIdx,
				"chunk", chunkNum, "error", err)
			return nil // dead host
		}

		logging.Info("sendCatchUp chunk response", "subsystem", "finalize", "escrow", s.escrowID,
			"nonce", nonce, "host", hostIdx,
			"chunk", chunkNum,
			"resp_nonce", resp.Nonce, "has_sig", resp.StateSig != nil)

		s.mu.Lock()
		if err := s.processResponse(hostIdx, resp, chunkNonce); err != nil {
			s.mu.Unlock()
			return err
		}
		s.logSignatureProgress(resp.Nonce)
		s.mu.Unlock()

		// Skip forward: if the host is already ahead of what we're about
		// to send (e.g. it caught up via gossip), jump to the chunk that
		// contains resp.Nonce+1 to avoid sending diffs the host already has.
		nextChunkIdx := chunkIdx + catchUpChunkSize
		if resp.Nonce > chunkNonce {
			skipTo := 0
			for i, d := range catchUp {
				if d.Nonce > resp.Nonce {
					skipTo = i
					break
				}
			}
			if skipTo > nextChunkIdx {
				skippedChunks := (skipTo - nextChunkIdx) / catchUpChunkSize
				logging.Info("sendCatchUp skip-forward", "subsystem", "finalize", "escrow", s.escrowID,
					"nonce", nonce, "host", hostIdx,
					"resp_nonce", resp.Nonce,
					"skipping_from_idx", nextChunkIdx, "to_idx", skipTo,
					"skipped_chunks", skippedChunks)
				nextChunkIdx = skipTo
			}
		}
		chunkIdx = nextChunkIdx
	}

	return nil
}

// Finalize completes the round in three phases.
//
// Phase A (N iterations): The first diff carries MsgFinalizeRound plus any
// pending txs. Each subsequent diff carries txs returned by the previous
// host's response. Hosts see Finalizing for the first time and produce
// MsgRevealSeed in their mempool.
//
// Phase A+1 (1 iteration): Drains the last host's MsgRevealSeed that
// remained in pendingTxs after Phase A. This is the final nonce that
// carries any txs. After this, state is frozen.
//
// Phase B (N iterations): Pure propagation + signature collection. No new
// diffs created. Sends catch-up diffs so every host reaches the final
// nonce and signs the same state.
func (s *Session) Finalize(ctx context.Context) error {
	if !s.finalizeInFlight.CompareAndSwap(false, true) {
		return fmt.Errorf("finalize already in progress")
	}
	defer s.finalizeInFlight.Store(false)

	// Guard: if already settled, try to collect missing signatures instead
	// of running the full finalize protocol again.
	phase := s.sm.Phase()
	threshold := s.sm.QuorumThreshold()
	if phase == types.PhaseSettlement {
		if s.hasQuorum(s.nonce, threshold) {
			logging.Info("finalize: already settled with quorum", "subsystem", "finalize", "escrow", s.escrowID,
				"nonce", s.nonce)
			return nil
		}
		logging.Info("finalize: settled but missing quorum, collecting signatures",
			"subsystem", "finalize", "escrow", s.escrowID, "nonce", s.nonce)
		weight, _, _ := s.CollectSignatures(ctx, s.nonce)
		if weight < threshold {
			return fmt.Errorf("insufficient signatures: %d/%d weight", weight, threshold)
		}
		return nil
	}
	if phase == types.PhaseFinalizing {
		return fmt.Errorf("finalize already in progress (phase=finalizing, nonce=%d)", s.nonce)
	}

	n := len(s.group)

	logging.Info("finalize started", "subsystem", "finalize", "escrow", s.escrowID,
		"group_size", n, "current_nonce", s.nonce,
		"total_slots", s.sm.TotalSlots(), "threshold", threshold)

	finalizeTx := &types.DevshardTx{Tx: &types.DevshardTx_FinalizeRound{
		FinalizeRound: &types.MsgFinalizeRound{},
	}}

	// Phase A: N diffs collecting remaining txs. First carries MsgFinalizeRound.
	logging.Info("finalize phase A: collecting reveals", "subsystem", "finalize", "escrow", s.escrowID,
		"rounds", n)
	for i := 0; i < n; i++ {
		var extra []*types.DevshardTx
		if i == 0 {
			extra = []*types.DevshardTx{finalizeTx}
		}
		if err := s.sendDiffRound(ctx, extra); err != nil {
			return err
		}
	}

	// Phase A+1: drain the last host's reveal.
	logging.Info("finalize phase A+1: draining last reveal", "subsystem", "finalize", "escrow", s.escrowID)
	if err := s.sendDiffRound(ctx, nil); err != nil {
		return err
	}

	// Phase B: collect signatures with retries.
	finalNonce := s.nonce
	weight, _, _ := s.CollectSignatures(ctx, finalNonce)

	logging.Info("finalize quorum check", "subsystem", "finalize", "escrow", s.escrowID,
		"final_nonce", finalNonce, "sig_weight", weight,
		"threshold", threshold, "total_slots", s.sm.TotalSlots())

	if weight < threshold {
		logging.Error("finalize failed: insufficient signatures", "subsystem", "finalize", "escrow", s.escrowID,
			"final_nonce", finalNonce, "sig_weight", weight, "threshold", threshold)
		return fmt.Errorf("insufficient signatures: %d/%d weight", weight, threshold)
	}

	logging.Info("finalize complete", "subsystem", "finalize", "escrow", s.escrowID,
		"final_nonce", finalNonce, "sig_weight", weight)
	return nil
}

// signDiff builds and signs a diff with the given nonce, txs, and post_state_root.
func (s *Session) signDiff(nonce uint64, txs []*types.DevshardTx, postStateRoot []byte) (types.Diff, error) {
	content := state.BuildDiffContent(s.escrowID, nonce, txs, postStateRoot)
	data, err := proto.Marshal(content)
	if err != nil {
		return types.Diff{}, fmt.Errorf("marshal diff content: %w", err)
	}
	sig, err := s.signer.Sign(data)
	if err != nil {
		return types.Diff{}, fmt.Errorf("sign diff: %w", err)
	}
	return types.Diff{Nonce: nonce, Txs: txs, UserSig: sig, PostStateRoot: postStateRoot}, nil
}

// devshardTxKey returns a dedup key for host-proposed txs.
// Returns "" for user-proposed types (start, finalize, timeout).
func devshardTxKey(tx *types.DevshardTx) string {
	switch inner := tx.GetTx().(type) {
	case *types.DevshardTx_FinishInference:
		return fmt.Sprintf("finish:%d", inner.FinishInference.InferenceId)
	case *types.DevshardTx_ConfirmStart:
		return fmt.Sprintf("confirm:%d", inner.ConfirmStart.InferenceId)
	case *types.DevshardTx_Validation:
		return fmt.Sprintf("validation:%d:%d", inner.Validation.InferenceId, inner.Validation.ValidatorSlot)
	case *types.DevshardTx_ValidationVote:
		return fmt.Sprintf("vote:%d:%d", inner.ValidationVote.InferenceId, inner.ValidationVote.VoterSlot)
	case *types.DevshardTx_RevealSeed:
		return fmt.Sprintf("reveal_seed:%d", inner.RevealSeed.SlotId)
	default:
		return ""
	}
}

// addPendingTx appends tx to pendingTxs if not a duplicate.
func (s *Session) addPendingTx(tx *types.DevshardTx) {
	key := devshardTxKey(tx)
	if key != "" {
		if _, dup := s.pendingTxKeys[key]; dup {
			return
		}
		s.pendingTxKeys[key] = struct{}{}
	}
	s.pendingTxs = append(s.pendingTxs, tx)
}

const maxPendingTxKeys = 100_000

// clearPendingTxs resets the pending tx slice. The dedup key set is preserved
// so that txs already applied in earlier diffs are not re-added from another
// host's mempool. The key set is bulk-cleared only when it exceeds the cap.
func (s *Session) clearPendingTxs() {
	s.pendingTxs = nil
	if len(s.pendingTxKeys) > maxPendingTxKeys {
		clear(s.pendingTxKeys)
	}
}

func (s *Session) Signatures() map[uint64]map[uint32][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.signatures
}

func (s *Session) Nonce() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nonce
}

func (s *Session) Diffs() []types.Diff {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.diffs
}

func (s *Session) PendingTxs() []*types.DevshardTx {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingTxs
}

func (s *Session) StateMachine() *state.StateMachine { return s.sm }

// sigWeight computes the slot-weighted signature count for a set of slot signatures,
// deduplicating by validator address. Caller must hold s.mu.
func (s *Session) sigWeight(sigs map[uint32][]byte) uint32 {
	counted := make(map[string]bool, len(s.addrToSlots))
	var weight uint32
	for slotID := range sigs {
		addr := s.sm.SlotAddress(slotID)
		if counted[addr] {
			continue
		}
		counted[addr] = true
		weight += s.sm.AddressSlotCount(addr)
	}
	return weight
}

// hasQuorum returns true if signatures at the given nonce meet the threshold.
// Thread-safe.
func (s *Session) hasQuorum(nonce uint64, threshold uint32) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sigs, ok := s.signatures[nonce]
	if !ok {
		return false
	}
	return s.sigWeight(sigs) >= threshold
}

// getFinalizeClients returns a client list with admission control stripped.
// Built lazily and cached on s.finalizeClients. Each client that implements
// a ClearAdmission method gets a shallow copy with admission disabled;
// others are used as-is.
func (s *Session) getFinalizeClients() []HostClient {
	if s.finalizeClients != nil {
		return s.finalizeClients
	}
	type admissionBypasser interface {
		WithoutAdmission() any
	}
	s.finalizeClients = make([]HostClient, len(s.clients))
	for i, c := range s.clients {
		if b, ok := c.(admissionBypasser); ok {
			if hc, ok := b.WithoutAdmission().(HostClient); ok {
				s.finalizeClients[i] = hc
				continue
			}
		}
		s.finalizeClients[i] = c
	}
	return s.finalizeClients
}

// fetchSignature tries to retrieve an already-stored signature from the host
// via the SignatureFetcher interface (GET /signatures?nonce=N). This avoids
// sending diffs when the host already signed the state via gossip.
// Returns true if a signature was successfully fetched and stored.
func (s *Session) fetchSignature(ctx context.Context, hostIdx int, nonce uint64, client HostClient) bool {
	fetcher, ok := client.(SignatureFetcher)
	if !ok {
		return false
	}

	sigs, err := fetcher.GetSignatures(ctx, nonce)
	if err != nil {
		logging.Info("fetchSignature GET failed", "subsystem", "finalize", "escrow", s.escrowID,
			"nonce", nonce, "host", hostIdx, "error", err)
		return false
	}
	if len(sigs) == 0 {
		return false
	}

	expectedAddr := s.group[hostIdx].ValidatorAddress
	s.mu.Lock()
	defer s.mu.Unlock()

	for slotID := range sigs {
		addr := s.sm.SlotAddress(slotID)
		if addr != expectedAddr {
			continue
		}
		if _, ok := s.signatures[nonce]; !ok {
			s.signatures[nonce] = make(map[uint32][]byte)
		}
		for _, slot := range s.addrToSlots[expectedAddr] {
			s.signatures[nonce][slot] = sigs[slotID]
		}
		logging.Info("fetched existing signature", "subsystem", "finalize", "escrow", s.escrowID,
			"nonce", nonce, "host", hostIdx, "address", expectedAddr)
		s.logSignatureProgress(nonce)
		return true
	}
	return false
}

// CollectSignatures actively polls all hosts to collect signatures for the
// given nonce. For each host: tries the cheap GET /signatures first, then
// falls back to sending catch-up diffs. Retries failed hosts with backoff.
// Uses per-host timeouts independent of the parent context.
// Returns the signature status at that nonce after collection. Thread-safe.
func (s *Session) CollectSignatures(ctx context.Context, nonce uint64) (weight, threshold, total uint32) {
	total = s.sm.TotalSlots()
	threshold = s.sm.QuorumThreshold()
	n := len(s.group)

	logging.Info("collecting signatures", "subsystem", "finalize", "escrow", s.escrowID,
		"nonce", nonce, "hosts", n, "threshold", threshold)

	finClients := s.getFinalizeClients()

	type hostEntry struct {
		idx  int
		addr string
	}
	seen := make(map[string]bool)
	var hosts []hostEntry
	for i := 0; i < n; i++ {
		addr := s.group[i].ValidatorAddress
		if seen[addr] {
			continue
		}
		seen[addr] = true
		hosts = append(hosts, hostEntry{i, addr})
	}

	// Fan out catch-up to all missing hosts in parallel. Each goroutine
	// tries GET /signatures first, then falls back to chunked sendCatchUp.
	// A shared context is cancelled as soon as quorum is reached so
	// in-progress catch-ups stop between chunks.
	collectCtx, collectCancel := context.WithCancel(ctx)
	defer collectCancel()

	// Filter to hosts that don't already have a signature at the final nonce.
	var missing []hostEntry
	s.mu.Lock()
	for _, h := range hosts {
		hasSig := false
		if sigs, ok := s.signatures[nonce]; ok {
			for _, slot := range s.addrToSlots[h.addr] {
				if _, ok := sigs[slot]; ok {
					hasSig = true
					break
				}
			}
		}
		if !hasSig {
			missing = append(missing, h)
		}
	}
	s.mu.Unlock()

	logging.Info("collecting signatures: fan-out", "subsystem", "finalize", "escrow", s.escrowID,
		"nonce", nonce, "missing_hosts", len(missing), "total_hosts", len(hosts))

	var wg sync.WaitGroup
	for _, h := range missing {
		wg.Add(1)
		go func(h hostEntry) {
			defer wg.Done()

			for attempt := 0; attempt <= s.signatureCollectMaxRetries; attempt++ {
				if s.hasQuorum(nonce, threshold) {
					return
				}
				if collectCtx.Err() != nil {
					return
				}

				if attempt > 0 {
					delay := s.signatureCollectBaseDelay * time.Duration(1<<(attempt-1))
					if delay > 30*time.Second {
						delay = 30 * time.Second
					}
					logging.Info("collect signature retry", "subsystem", "finalize", "escrow", s.escrowID,
						"nonce", nonce, "host", h.idx, "attempt", attempt, "delay", delay.String())
					select {
					case <-collectCtx.Done():
						return
					case <-time.After(delay):
					}
				}

				// Already got it on a previous retry?
				s.mu.Lock()
				hasSig := false
				if sigs, ok := s.signatures[nonce]; ok {
					for _, slot := range s.addrToSlots[h.addr] {
						if _, ok := sigs[slot]; ok {
							hasSig = true
							break
						}
					}
				}
				s.mu.Unlock()
				if hasSig {
					return
				}

				fetchCtx, fetchCancel := context.WithTimeout(collectCtx, s.signatureCollectHostTimeout)
				if s.fetchSignature(fetchCtx, h.idx, nonce, finClients[h.idx]) {
					fetchCancel()
					if s.hasQuorum(nonce, threshold) {
						collectCancel()
					}
					return
				}
				fetchCancel()

				if err := s.sendCatchUpWith(collectCtx, h.idx, finClients[h.idx]); err != nil {
					logging.Warn("collect signatures: send catch-up error", "subsystem", "finalize", "escrow", s.escrowID,
						"nonce", nonce, "host", h.idx, "attempt", attempt, "error", err)
				}

				if s.hasQuorum(nonce, threshold) {
					collectCancel()
					return
				}
			}
		}(h)
	}
	wg.Wait()

	s.mu.Lock()
	defer s.mu.Unlock()
	if sigs, ok := s.signatures[nonce]; ok {
		weight = s.sigWeight(sigs)
	}

	// Log which hosts are still missing signatures.
	if weight < threshold {
		var missing []string
		for _, h := range hosts {
			hasSig := false
			if sigs, ok := s.signatures[nonce]; ok {
				for _, slot := range s.addrToSlots[h.addr] {
					if _, ok := sigs[slot]; ok {
						hasSig = true
						break
					}
				}
			}
			if !hasSig {
				missing = append(missing, fmt.Sprintf("%d(%s)", h.idx, shortAddress(h.addr)))
			}
		}
		logging.Warn("collect signatures: missing hosts after all attempts", "subsystem", "finalize", "escrow", s.escrowID,
			"nonce", nonce, "weight", weight, "threshold", threshold,
			"missing_count", len(missing), "missing", strings.Join(missing, ","))
	}

	return weight, threshold, total
}

// SignatureStatusEntry describes signature accumulation for a single nonce.
type SignatureStatusEntry struct {
	Nonce     uint64 `json:"nonce"`
	SigWeight uint32 `json:"sig_weight"`
	Total     uint32 `json:"total_slots"`
	HasQuorum bool   `json:"has_quorum"`
}

// SignatureStatus returns per-nonce effective signature weight and the highest
// nonce that has reached 2/3+1 quorum. Thread-safe.
func (s *Session) SignatureStatus() (entries []SignatureStatusEntry, highestQuorum uint64, hasAny bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.signatureStatusLocked()
}

// signatureStatusLocked computes signature status using the monotonic property:
// a validator that signed nonce M implicitly accepted all nonces <= M.
// For each nonce N, effective weight = sum of slots for all validators whose
// highest signed nonce >= N.
// Caller must hold s.mu.
func (s *Session) signatureStatusLocked() (entries []SignatureStatusEntry, highestQuorum uint64, hasAny bool) {
	total := s.sm.TotalSlots()
	threshold := s.sm.QuorumThreshold()

	addrMaxNonce := make(map[string]uint64)
	for nonce, slotSigs := range s.signatures {
		for slotID := range slotSigs {
			addr := s.sm.SlotAddress(slotID)
			if nonce > addrMaxNonce[addr] {
				addrMaxNonce[addr] = nonce
			}
		}
	}

	nonces := make([]uint64, 0, len(s.signatures))
	for n := range s.signatures {
		nonces = append(nonces, n)
	}
	sort.Slice(nonces, func(i, j int) bool { return nonces[i] < nonces[j] })

	nonceWeight := make(map[uint64]uint32, len(addrMaxNonce))
	for addr, maxN := range addrMaxNonce {
		nonceWeight[maxN] += s.sm.AddressSlotCount(addr)
	}

	entries = make([]SignatureStatusEntry, len(nonces))
	var cumWeight uint32
	for i := len(nonces) - 1; i >= 0; i-- {
		n := nonces[i]
		cumWeight += nonceWeight[n]
		entries[i] = SignatureStatusEntry{
			Nonce:     n,
			SigWeight: cumWeight,
			Total:     total,
			HasQuorum: cumWeight >= threshold,
		}
		if cumWeight >= threshold && (!hasAny || n > highestQuorum) {
			highestQuorum = n
			hasAny = true
		}
	}

	return entries, highestQuorum, hasAny
}

// logSignatureProgress logs signature weight at the given nonce.
// Caller must hold s.mu.
func (s *Session) logSignatureProgress(nonce uint64) {
	slotSigs, ok := s.signatures[nonce]
	if !ok {
		return
	}
	weight := s.sigWeight(slotSigs)
	threshold := s.sm.QuorumThreshold()

	logging.Info("signature progress", "subsystem", "finalize", "escrow", s.escrowID,
		"nonce", nonce, "weight", weight,
		"threshold", threshold, "total", s.sm.TotalSlots())
}

// AddPendingTimeoutTx adds a MsgTimeoutInference to the pending tx queue.
func (s *Session) AddPendingTimeoutTx(inferenceID uint64, reason types.TimeoutReason, votes []*types.TimeoutVote) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addPendingTx(&types.DevshardTx{
		Tx: &types.DevshardTx_TimeoutInference{TimeoutInference: &types.MsgTimeoutInference{
			InferenceId: inferenceID,
			Reason:      reason,
			Votes:       votes,
		}},
	})
}

// SendPendingDiff creates a diff from pending txs (no new MsgStartInference),
// applies it locally, and sends it to the next host. Used for timeout submission.
func (s *Session) SendPendingDiff(ctx context.Context) error {
	s.mu.Lock()
	diff, hostIdx, err := s.composeDiffLocked(nil)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	catchUp := s.diffsForHost(hostIdx)
	s.mu.Unlock()

	resp, err := s.clients[hostIdx].Send(ctx, host.HostRequest{Diffs: catchUp, Nonce: diff.Nonce}, nil, nil)
	if err != nil {
		return fmt.Errorf("send timeout diff to host %d: %w", hostIdx, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.processResponse(hostIdx, resp, diff.Nonce)
}

// TimeoutVerifiers returns a map of host index -> TimeoutVerifier for all
// hosts whose underlying client implements TimeoutVerifier. This gives the
// proxy access to verifier instances for timeout vote collection.
func (s *Session) TimeoutVerifiers() map[int]TimeoutVerifier {
	result := make(map[int]TimeoutVerifier, len(s.clients))
	for i, c := range s.clients {
		if tv, ok := c.(TimeoutVerifier); ok {
			result[i] = tv
		}
	}
	return result
}

// Clients returns the underlying host clients. Useful for constructing
// timeout verifiers or other operations that need direct host access.
func (s *Session) Clients() []HostClient { return s.clients }

// HostLabel returns the short validator address for the given host index.
func (s *Session) HostLabel(hostIdx int) string {
	if hostIdx < 0 || hostIdx >= len(s.group) {
		return fmt.Sprintf("%d", hostIdx)
	}
	return shortAddress(s.group[hostIdx].ValidatorAddress)
}

// SetParticipantKeys overrides the per-slot participant identifiers
// used by admission control, the picker's PoC/throttle checks, and
// CapacityState lookups. The canonical contract is that each entry is
// the slot's gonka validator address (matching the chain's
// participant Index). NewSession already initializes the slice this
// way; callers should only override when the underlying group changes
// (e.g. recovery or test wiring), and must keep the same gonka-address
// scheme so keys align across subsystems.
func (s *Session) SetParticipantKeys(keys []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(keys) != len(s.group) {
		return
	}
	s.participantKeys = append([]string(nil), keys...)
}

// ParticipantKeys returns the unique participant identifiers (gonka
// validator addresses) for this session, deduplicating multi-slot
// validators so the result is one entry per physical participant.
func (s *Session) ParticipantKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[string]struct{}, len(s.participantKeys))
	keys := make([]string, 0, len(s.participantKeys))
	for i, key := range s.participantKeys {
		if key == "" && i < len(s.group) {
			key = s.group[i].ValidatorAddress
		}
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

// HostParticipantKey returns the canonical participant identifier
// (gonka validator address) for one host slot. Multiple slots backed
// by the same validator return the same key.
func (s *Session) HostParticipantKey(hostIdx int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hostParticipantKeyLocked(hostIdx)
}

// HostParticipantKeyList returns one participant identifier per slot
// (length == group size), preserving duplicates so callers can compute
// per-slot statistics like how many slots a single host occupies in
// this session. Empty entries are filled in with the slot's validator
// address as a fallback to mirror HostParticipantKey semantics.
func (s *Session) HostParticipantKeyList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.group))
	for i := range s.group {
		out[i] = s.hostParticipantKeyLocked(i)
	}
	return out
}

// hostParticipantKeyLocked is the lock-free body of HostParticipantKey.
// Caller must hold s.mu. Used by PrepareInferenceFn so it can build a
// HostBinding while still holding the nonce lock without re-entering it.
func (s *Session) hostParticipantKeyLocked(hostIdx int) string {
	if hostIdx < 0 || hostIdx >= len(s.group) {
		return ""
	}
	if hostIdx < len(s.participantKeys) && strings.TrimSpace(s.participantKeys[hostIdx]) != "" {
		return strings.TrimSpace(s.participantKeys[hostIdx])
	}
	return strings.TrimSpace(s.group[hostIdx].ValidatorAddress)
}

// IsNonceFinished returns true if ProcessResponse observed MsgFinishInference
// for the given nonce. Must be called after ProcessResponse.
func (s *Session) IsNonceFinished(nonce uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o, ok := s.nonceStates[nonce]; ok {
		return o.finished
	}
	return false
}

// HandleTimeout handles the full protocol timeout for a failed inference nonce:
// waits for the protocol deadline, collects timeout votes, and submits
// MsgTimeoutInference if sufficient votes are gathered.
// sendTime is when the nonce's network call started.
func (s *Session) HandleTimeout(ctx context.Context, nonce uint64, sendTime time.Time, payload *host.InferencePayload) (TimeoutResult, error) {
	s.mu.Lock()
	cfg := s.sm.SnapshotState().Config
	confirmedAt := int64(0)
	if o, ok := s.nonceStates[nonce]; ok {
		confirmedAt = o.confirmedAt
	}
	hostIdx := int(nonce % uint64(len(s.group)))
	hostID := s.HostLabel(hostIdx)
	s.mu.Unlock()

	logFields := func(extra ...any) []any {
		base := []any{"escrow", s.escrowID, "nonce", nonce, "host", hostID}
		return append(base, extra...)
	}

	var reason types.TimeoutReason
	var deadline time.Time
	if confirmedAt > 0 {
		deadline = time.Unix(confirmedAt, 0).Add(
			time.Duration(cfg.ExecutionTimeout)*time.Second + TimeoutBuffer)
		if !sleepUntilDeadlineWithHeartbeat(ctx, deadline, func() {
			logging.Stage(ctx, "timeout_waiting", logFields("reason", "execution", "remaining_ms", time.Until(deadline).Milliseconds())...)
		}) {
			return TimeoutResult{}, ctx.Err()
		}
		reason = types.TimeoutReason_TIMEOUT_REASON_EXECUTION
	} else {
		deadline = sendTime.Add(
			time.Duration(cfg.RefusalTimeout)*time.Second + TimeoutBuffer)
		if !sleepUntilDeadlineWithHeartbeat(ctx, deadline, func() {
			logging.Stage(ctx, "timeout_waiting", logFields("reason", "refused", "remaining_ms", time.Until(deadline).Milliseconds())...)
		}) {
			return TimeoutResult{}, ctx.Err()
		}
		reason = types.TimeoutReason_TIMEOUT_REASON_REFUSED
	}

	result := TimeoutResult{Reason: timeoutReasonLogLabel(reason)}

	logging.Stage(ctx, "timeout_started", logFields("reason", result.Reason)...)

	verifiers := s.TimeoutVerifiers()
	storedDiffs := s.Diffs()

	votes, err := s.CollectTimeoutVotes(ctx, nonce, reason, payload, verifiers, storedDiffs)
	if err != nil {
		return result, fmt.Errorf("collect timeout votes: %w", err)
	}

	if s.HasSufficientTimeoutVotes(votes) {
		s.AddPendingTimeoutTx(nonce, reason, votes)
		if err := s.SendPendingDiff(ctx); err != nil {
			logging.Stage(ctx, "timeout_diff_send_failed", logFields("reason", result.Reason, "error", err)...)
			return result, fmt.Errorf("send timeout diff: %w", err)
		}
		logging.Stage(ctx, "timeout_completed", logFields("reason", result.Reason)...)
		return result, fmt.Errorf("inference %d timed out: %s", nonce, reason)
	}

	logging.Stage(ctx, "timeout_insufficient_votes", logFields("reason", result.Reason)...)
	return result, fmt.Errorf("inference %d timed out but insufficient votes", nonce)
}

// TimeoutHeartbeatInterval controls how often timeout_waiting logs are emitted.
var TimeoutHeartbeatInterval = time.Minute

func sleepUntilDeadlineWithHeartbeat(ctx context.Context, deadline time.Time, heartbeat func()) bool {
	d := time.Until(deadline)
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	var heartbeatC <-chan time.Time
	var ticker *time.Ticker
	if heartbeat != nil && TimeoutHeartbeatInterval > 0 {
		ticker = time.NewTicker(TimeoutHeartbeatInterval)
		defer ticker.Stop()
		heartbeatC = ticker.C
	}
	select {
	case <-timer.C:
		return true
	case <-heartbeatC:
		heartbeat()
		return sleepUntilDeadlineWithHeartbeat(ctx, deadline, heartbeat)
	case <-ctx.Done():
		return false
	}
}

func shortAddress(addr string) string {
	if len(addr) <= 8 {
		return addr
	}
	return addr[len(addr)-8:]
}

// Close releases the underlying storage, if any. Safe to call multiple times.
func (s *Session) Close() error {
	if s.store != nil {
		return s.store.Close()
	}
	return nil
}

// TimeoutVerifier contacts a host for timeout verification votes.
type TimeoutVerifier interface {
	VerifyTimeout(ctx context.Context, inferenceID uint64, reason types.TimeoutReason, payload *host.InferencePayload, diffs []types.Diff) (accept bool, sig []byte, voterSlot uint32, err error)
}

func timeoutReasonLogLabel(reason types.TimeoutReason) string {
	switch reason {
	case types.TimeoutReason_TIMEOUT_REASON_EXECUTION:
		return "execution"
	case types.TimeoutReason_TIMEOUT_REASON_REFUSED:
		return "refused"
	default:
		return "unknown"
	}
}

// CollectTimeoutVotes contacts non-executor hosts to collect signed votes.
// Returns votes for inclusion in MsgTimeoutInference.
// Deduplicates verifiers by validator address to avoid duplicate votes
// when the same validator occupies multiple slots.
// Diffs are forwarded to verifiers so they can catch up to the inference nonce.
func (s *Session) CollectTimeoutVotes(
	ctx context.Context,
	inferenceID uint64,
	reason types.TimeoutReason,
	payload *host.InferencePayload,
	verifiers map[int]TimeoutVerifier, // hostIdx -> verifier
	diffs []types.Diff,
) ([]*types.TimeoutVote, error) {
	// Cancel all in-flight verifier RPCs (and unblock any goroutines still
	// waiting in the per-verifier queue) once we return — typically because
	// the vote-weight threshold was met early. Without this, leftover
	// goroutines would keep occupying verifier-host queue slots and consume
	// outbound connections we no longer need.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Determine executor slot and resolve its validator address.
	executorIdx := int(inferenceID % uint64(len(s.group)))
	executorAddr := s.group[executorIdx].ValidatorAddress

	// Dedup verifiers by address to avoid duplicate votes from multi-slot validators.
	// Pre-seed the executor's address so ALL slots owned by that validator are excluded,
	// not just the single executor index. This prevents a multi-slot executor from
	// voting on its own timeout through a different slot.
	type addrVerifier struct {
		idx          int
		verifier     TimeoutVerifier
		verifierAddr string
	}
	seen := make(map[string]bool)
	seen[executorAddr] = true
	var deduped []addrVerifier
	for idx, v := range verifiers {
		addr := s.group[idx].ValidatorAddress
		if seen[addr] {
			continue
		}
		seen[addr] = true
		deduped = append(deduped, addrVerifier{idx: idx, verifier: v, verifierAddr: addr})
	}

	type voteResult struct {
		vote         *types.TimeoutVote
		err          error
		verifierIdx  int
		verifierAddr string
	}

	logFields := func(hostAddr string, extra ...any) []any {
		base := []any{
			"escrow", s.escrowID,
			"nonce", inferenceID,
			"reason", timeoutReasonLogLabel(reason),
		}
		if hostAddr != "" {
			base = append(base, "host", shortAddress(hostAddr))
		}
		return append(base, extra...)
	}

	results := make(chan voteResult, len(deduped))
	for _, av := range deduped {
		logging.Stage(ctx, "timeout_vote_requested", logFields(av.verifierAddr)...)
		go func(av addrVerifier) {
			// Serialize VerifyTimeout per verifier host so a single bad
			// executor producing many simultaneous timeouts cannot open
			// MaxConcurrentVerifierRPCs+ connections to any one verifier.
			// Cap the wait with VerifierQueueWaitTimeout so goroutines
			// don't accumulate indefinitely when a verifier is hung,
			// and so a vote that's already stale by the time it could
			// dequeue is suppressed instead of producing a stale RPC.
			queueStart := time.Now()
			waitCtx, waitCancel := context.WithTimeout(ctx, VerifierQueueWaitTimeout)
			err := s.verifierQueue.acquire(waitCtx, av.verifierAddr)
			waitCancel()
			if err != nil {
				logging.Stage(ctx, "timeout_vote_queue_expired",
					logFields(av.verifierAddr,
						"wait_ms", time.Since(queueStart).Milliseconds(),
						"wait_timeout_ms", VerifierQueueWaitTimeout.Milliseconds(),
						"error", err,
					)...,
				)
				results <- voteResult{err: err, verifierIdx: av.idx, verifierAddr: av.verifierAddr}
				return
			}
			waitMs := time.Since(queueStart).Milliseconds()
			if waitMs > 0 {
				logging.Stage(ctx, "timeout_vote_dequeued",
					logFields(av.verifierAddr, "wait_ms", waitMs)...,
				)
			}
			defer s.verifierQueue.release(av.verifierAddr)

			// Defense-in-depth: the parent ctx may have been cancelled
			// (e.g. CollectTimeoutVotes early-exit on vote-weight
			// threshold met) during the final moments of the queue wait.
			// Skip the RPC so we neither waste the verifier's time nor
			// the slot we just acquired.
			if err := ctx.Err(); err != nil {
				results <- voteResult{err: err, verifierIdx: av.idx, verifierAddr: av.verifierAddr}
				return
			}

			accept, sig, voterSlot, err := av.verifier.VerifyTimeout(ctx, inferenceID, reason, payload, diffs)
			if err != nil {
				results <- voteResult{err: err, verifierIdx: av.idx, verifierAddr: av.verifierAddr}
				return
			}
			if !accept {
				results <- voteResult{verifierIdx: av.idx, verifierAddr: av.verifierAddr} // nil vote, no error
				return
			}
			results <- voteResult{vote: &types.TimeoutVote{
				VoterSlot: voterSlot,
				Accept:    true,
				Signature: sig,
			}, verifierIdx: av.idx, verifierAddr: av.verifierAddr}
		}(addrVerifier{
			idx:          av.idx,
			verifier:     av.verifier,
			verifierAddr: av.verifierAddr,
		})
	}

	var votes []*types.TimeoutVote
	expected := len(deduped)

	voteThreshold := s.sm.VoteThreshold()
	var accWeight uint32
	var errors, rejects int
	for i := 0; i < expected; i++ {
		res := <-results
		if res.err != nil {
			errors++
			logging.Stage(ctx, "timeout_vote_result",
				logFields(
					res.verifierAddr,
					"outcome", "error",
					"running_weight", accWeight,
					"threshold", voteThreshold,
					"error", res.err,
				)...,
			)
			logging.Debug("timeout vote error",
				"subsystem", "session", "inference_id", inferenceID, "error", res.err)
			continue // skip failed hosts
		}
		if res.vote != nil {
			votes = append(votes, res.vote)
			voterAddr := s.sm.SlotAddress(res.vote.VoterSlot)
			weight := s.sm.AddressSlotCount(voterAddr)
			accWeight += weight
			logging.Stage(ctx, "timeout_vote_result",
				logFields(
					res.verifierAddr,
					"outcome", "accept",
					"voter_slot", res.vote.VoterSlot,
					"voter", shortAddress(voterAddr),
					"weight", weight,
					"running_weight", accWeight,
					"threshold", voteThreshold,
				)...,
			)
		} else {
			rejects++
			logging.Stage(ctx, "timeout_vote_result",
				logFields(
					res.verifierAddr,
					"outcome", "reject",
					"running_weight", accWeight,
					"threshold", voteThreshold,
				)...,
			)
		}
		if accWeight > voteThreshold {
			break
		}
	}
	logging.Stage(ctx, "timeout_vote_tally",
		logFields(
			"",
			"accept", len(votes),
			"weight", accWeight,
			"reject", rejects,
			"errors", errors,
			"threshold", voteThreshold,
			"verifiers", expected,
			"sufficient", accWeight > voteThreshold,
		)...,
	)
	logging.Debug("timeout vote collection",
		"subsystem", "session", "inference_id", inferenceID,
		"accept", len(votes), "weight", accWeight,
		"reject", rejects, "errors", errors,
		"threshold", voteThreshold, "verifiers", expected)

	return votes, nil
}

// HasSufficientTimeoutVotes returns true if the accept votes exceed the vote threshold.
func (s *Session) HasSufficientTimeoutVotes(votes []*types.TimeoutVote) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	threshold := s.sm.VoteThreshold()
	var accWeight uint32
	for _, v := range votes {
		if v.Accept {
			addr := s.sm.SlotAddress(v.VoterSlot)
			accWeight += s.sm.AddressSlotCount(addr)
		}
	}
	return accWeight > threshold
}
