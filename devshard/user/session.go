package user

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"devshard"
	"devshard/heightsync"
	"devshard/host"
	"devshard/logging"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/transport"
	"devshard/types"
)

// TimeoutBuffer is added to protocol deadlines so verifiers have
// passed their own deadline before the proxy fires the timeout.
var TimeoutBuffer = 5 * time.Second

// ErrNilHostResponse is returned when ProcessResponse is given a nil resp.
// A successful Send never yields nil without an error; treating nil as
// success would hide a caller bug on the streaming path.
var ErrNilHostResponse = errors.New("nil host response")

// MaxConcurrentVerifierRPCs caps how many simultaneous VerifyTimeout RPCs the
// proxy may have open against the same verifier host. CollectTimeoutVotes fans
// out one VerifyTimeout per verifier per timed-out nonce; the cap is
// process-wide via SharedVerifierQueue so K escrows cannot stack unbounded
// POSTs onto one validator.
//
// Execution-timeout verify is a cheap FinishInference probe (local mempool,
// then GET executor mempool). Cap 1 made a timeout wave wait 120s and drop
// votes that never left the gateway while one hung GetMempool held the slot.
// 32 lets concurrent rounds send; it is still a bound, not "one at a time".
// Refused-timeout verify is heavier (ChallengeReceipt) and shares this cap.
// Different verifiers are still hit in parallel.
var MaxConcurrentVerifierRPCs = 32

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

// VerifyTimeoutSlowLog is the elapsed wall time after which a sent
// VerifyTimeout is logged as timeout_vote_slow, whether it accepted,
// rejected, or failed.
var VerifyTimeoutSlowLog = 15 * time.Second

// inflightSnapshotLimit caps how many occupying RPCs are spelled out on
// timeout_vote_queue_expired. The count is still exact.
const inflightSnapshotLimit = 8

// nonceOutcome tracks protocol-relevant facts observed for a single inference nonce.
type nonceOutcome struct {
	confirmedAt int64
	finished    bool
}

// TimeoutResult reports what happened during timeout handling.
type TimeoutResult struct {
	Reason        string   // "execution", "refused", "error", or "" if deadline not reached
	Outcome       string   // how handling ended: skipped, vote_collection_failed, insufficient_votes, diff_send_failed, applied
	DetailReason  string   // why it ended that way, when the outcome alone is ambiguous
	Applied       bool     // MsgTimeoutInference reached the escrow state
	Votes         int      // accept votes collected
	Accepted      bool     // true when accept weight exceeded the vote threshold
	ResponseHash  []byte   // sha256 of the error-miss payload the gateway reconstructed; empty otherwise
	VerifyRejects []string // error-miss verifier reject causes
}

// ErrInferenceMissed is returned by HandleErrorMiss when a miss was applied
// on-chain. Callers must treat it as miss-path success, not as a
// vote-collection failure.
var ErrInferenceMissed = errors.New("inference missed")

// HasMsgFinish returns true if mempool contains MsgFinishInference for the given nonce.
func HasMsgFinish(txs []*types.DevshardTx, nonce uint64) bool {
	for _, tx := range txs {
		if fi := tx.GetFinishInference(); fi != nil && fi.InferenceId == nonce {
			return true
		}
	}
	return false
}

func HasMsgTimeout(txs []*types.DevshardTx, nonce uint64) bool {
	for _, tx := range txs {
		if timeout := tx.GetTimeoutInference(); timeout != nil && timeout.InferenceId == nonce {
			return true
		}
	}
	return false
}

// MarshalFinishTx returns proto bytes of the DevshardTx wrapping MsgFinishInference
// for inferenceID, or nil if none is present.
func MarshalFinishTx(txs []*types.DevshardTx, inferenceID uint64) []byte {
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		if fi := tx.GetFinishInference(); fi != nil && fi.InferenceId == inferenceID {
			b, err := proto.Marshal(tx)
			if err != nil {
				return nil
			}
			return b
		}
	}
	return nil
}

type HostClient interface {
	Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error)
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

func (c *InProcessClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
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
		receiptHandler(resp)
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

// GetSignatures implements SignatureFetcher for in-process hosts so Finalize
// can pull already-stored signatures without re-sending diffs.
func (c *InProcessClient) GetSignatures(_ context.Context, nonce uint64) (map[uint32][]byte, error) {
	if c == nil || c.Host == nil {
		return nil, fmt.Errorf("in-process client has no host")
	}
	return c.Host.GetSignatures(nonce)
}

// InferenceParams describes a new inference to send.
type InferenceParams struct {
	Model                 string
	Prompt                []byte
	InputLength           uint64
	MaxTokens             uint64
	ContextTotalHint      uint64
	StartedAt             int64
	Stream                bool
	ForceHeightSyncAnchor bool
	AffinityKey           string
}

// Payload is the single InferenceParams -> InferencePayload conversion. Every path that
// sends or re-sends a prompt goes through it, so none can drop the session id and land the
// re-execution in the shared, unsalted cache namespace.
func (p InferenceParams) Payload() *host.InferencePayload {
	return &host.InferencePayload{
		Prompt:      p.Prompt,
		Model:       p.Model,
		InputLength: p.InputLength,
		MaxTokens:   p.MaxTokens,
		StartedAt:   p.StartedAt,
		SessionID:   p.AffinityKey,
	}
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
	diffs           []types.Diff        // append-only log
	hostSyncNonce   map[int]uint64      // hostIdx -> last nonce sent
	pendingTxs      []*types.DevshardTx // from host mempools, for next diff
	// pendingTxKeys dedups the current pendingTxs slice by tx_type:id. It is
	// rebuilt from what compose retained, so a tx that failed to apply frees
	// its key again -- otherwise the first host to propose a bogus tx would
	// permanently shadow the honest tx sharing that key.
	pendingTxKeys map[string]struct{}
	// appliedTxKeys holds tx_type:id for txs that actually landed in a diff,
	// so another host's mempool copy is not re-queued after the fact. Only
	// this set survives across compose rounds.
	appliedTxKeys map[string]struct{}
	// pinnedFinishIDs holds inference IDs whose pending Finish and ErrorMiss
	// must not be drained by a concurrent composeDiffLocked (heartbeat,
	// PrepareInference, unrelated SendPendingDiff). HandleErrorMiss pins
	// the nonce for the vote round and compose so Finish+ErrorMiss land together.
	pinnedFinishIDs map[uint64]int
	signatures      map[uint64]map[uint32][]byte // nonce -> slotID -> sig
	store           storage.Storage              // optional persistent storage
	nonceStates     map[uint64]*nonceOutcome     // nonce -> protocol outcome
	verifierQueue   *verifierHostQueue           // per-verifier RPC limiter for timeout votes
	diffObserver    func(types.Diff)

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

	heightSyncK     uint64
	heightSyncSlots uint64

	heartbeatCfg   heightsync.HeartbeatConfig
	heartbeat      *heightsync.Heartbeat
	turnTracker    *heightsync.TurnTracker
	observedHeight func() (height uint64, hash []byte, ok bool)
	// heartbeatFlushLeft is the number of ack-carrying rounds still owed to the
	// open turn. The heartbeat cadence is wall clock, so the producer does not
	// wait for a block tick to collect acks.
	heartbeatFlushLeft int
	// clock drives the heartbeat cadence and is injectable for tests. It is
	// never written into Diff: turn records stay clock-free.
	clock func() time.Time

	// heartbeat loop. StartHeartbeatLoop is idempotent; Close
	// cancels it. heartbeatClosed is set first so a Start that races Close
	// does not spawn a goroutine after shutdown.
	heartbeatLoopOnce sync.Once
	heartbeatClosed   bool
	heartbeatStop     context.CancelFunc
	heartbeatDone     chan struct{}

	// heightSeedMu serializes seed state. The session loop (gate on) retries
	// forever after catalog admission; missed is not terminal.
	heightSeedMu              sync.Mutex
	heightSeedMissed          atomic.Bool
	heightSeedPhase           atomic.Uint32
	heightSeedCatalogAdmitted atomic.Bool
	heightSeedGateUntil       atomic.Int64
	heightSeedOK              map[int]struct{}
	heightSeedLast            map[int]seedSlotRecord
	heightSeedSlotCount       int
	heightSeedWaiters         chan struct{}
	requireHeightSeed         bool
	heightSeedLoopOnce        sync.Once
	heightSeedClosed          bool
	heightSeedStop            context.CancelFunc
	heightSeedDoneCh          chan struct{}

	lastContact   []time.Time
	lastPeerSeen  map[uint32][]byte
	lastSyncState map[uint32]string
	peerTips      *transport.HeightSyncPeerTips
	overlap       heightsync.ExchangeOverlap
	anchors       *heightsync.AnchorTally

	// heightSyncWired is set when courier height-sync is configured
	// (cadence K/slots or an injected peer-tip cache). Unwired sessions
	// never publish an operator view, so gateway scrapes skip them.
	heightSyncWired atomic.Bool
	// heightSyncView is the last published OperatorView. Collect and
	// GET /v1/debug/heightsync read this only — they must not take s.mu
	// (scrape must not stall behind persistDiffRetryLocked).
	heightSyncView atomic.Pointer[heightSyncPublished]
	// heightSyncPublishMu serializes snapshot+store so an older concurrent
	// publish cannot overwrite a newer one. Never hold s.mu while taking
	// this lock (publish runs after unlock).
	heightSyncPublishMu sync.Mutex
	heightSyncDirty     atomic.Bool
	heightSyncLastPubNs atomic.Int64
	heightSyncPubSeq    atomic.Uint64
	heightSyncPubCount  atomic.Uint64 // tests: successful installs
	// heightSyncFlush is the trailing edge of the publish throttle: one timer,
	// armed by a coalesced call, so the last update of a burst reaches the
	// cache within the throttle interval instead of waiting for the next
	// mutation. Guarded by heightSyncPublishMu.
	heightSyncFlush       *time.Timer
	heightSyncFlushClosed bool
	// heightSyncMinPublishNs is the min gap between publishes. 0 uses
	// heightSyncDefaultMinPublish. Tests may override via TestingOnly*.
	heightSyncMinPublishNs atomic.Int64
}

// SessionOption configures optional Session behavior.
type SessionOption func(*Session)

// WithStorage sets a persistent storage backend for the session.
// When set, diffs and signatures are persisted on each state transition.
func WithStorage(s storage.Storage) SessionOption {
	return func(sess *Session) { sess.store = s }
}

// WithHeightSyncCadence records K (and optional scheduler slots) for height-sync
// wiring. MsgForceHeightSyncTurn.slots_num always follows the escrow roster;
// applyForceHeightSyncTurn rejects any other value.
func WithHeightSyncCadence(k, slots uint64) SessionOption {
	return func(sess *Session) {
		sess.heightSyncK = k
		sess.heightSyncSlots = slots
	}
}

// SetHeightSyncCadence records K (and optional scheduler slots) after RecoverSession
// or ExtraClientConfig. Force-turn slots_num is always len(group); see
// heightSyncForceSlotsLocked.
func (s *Session) SetHeightSyncCadence(k, slots uint64) {
	s.mu.Lock()
	s.heightSyncK = k
	s.heightSyncSlots = slots
	s.mu.Unlock()
	if k > 0 || slots > 0 {
		s.heightSyncWired.Store(true)
		s.publishHeightSyncViewForce()
	}
}

// heightSyncForceSlotsLocked is the SM invariant: MsgForceHeightSyncTurn.slots_num
// must equal group size. Courier scheduler defaults (env unset → slots=1) must
// not override this or the tx is dropped.
func (s *Session) heightSyncForceSlotsLocked() uint64 {
	return uint64(len(s.group))
}

// WithHeartbeatConfig overrides compiled heartbeat defaults (tests / runtimeparams).
func WithHeartbeatConfig(cfg heightsync.HeartbeatConfig) SessionOption {
	return func(sess *Session) { sess.heartbeatCfg = cfg }
}

// WithHeartbeatClock overrides the wall clock driving the heartbeat cadence (tests).
func WithHeartbeatClock(now func() time.Time) SessionOption {
	return func(sess *Session) {
		if now != nil {
			sess.clock = now
		}
	}
}

// WithObservedHeight injects the height+hash source used by MaybeHeartbeat.
// Production uses ObservedHeightNow on the HTTP clients when this is unset.
func WithObservedHeight(fn func() (height uint64, hash []byte, ok bool)) SessionOption {
	return func(sess *Session) { sess.observedHeight = fn }
}

// SetObservedHeight replaces the height source (e.g. after RecoverSession).
func (s *Session) SetObservedHeight(fn func() (height uint64, hash []byte, ok bool)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observedHeight = fn
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
		appliedTxKeys:   make(map[string]struct{}),
		pinnedFinishIDs: make(map[uint64]int),
		signatures:      make(map[uint64]map[uint32][]byte),
		nonceStates:     make(map[uint64]*nonceOutcome),
		verifierQueue:   SharedVerifierQueue,
		diffObserver:    func(types.Diff) {},
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
	if sess.clock == nil {
		sess.clock = time.Now
	}
	sess.heartbeat = heightsync.NewHeartbeat(sess.heartbeatCfg)
	slots := uint64(len(group))
	sess.heartbeat.SetRoster(slots, 0)
	cfg := sess.heartbeat.Config()
	sess.turnTracker = heightsync.NewTurnTracker(slots, 0, cfg)
	sess.lastContact = make([]time.Time, len(group))
	sess.lastPeerSeen = make(map[uint32][]byte)
	sess.lastSyncState = make(map[uint32]string)
	sess.anchors = heightsync.NewAnchorTally(cfg.AckDeadlineBlocks, 0)
	if sess.heightSyncK > 0 || sess.heightSyncSlots > 0 {
		sess.heightSyncWired.Store(true)
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
	if resp == nil {
		return ErrNilHostResponse
	}
	// Verify state hash if the host returned one. Contact/overlap wait until
	// verification succeeds so a bad hash cannot inflate monitoring.
	if len(resp.StateHash) > 0 {
		var expected []byte
		if root, ok := s.postStateRootForNonce(resp.Nonce); ok {
			expected = root
		} else if resp.Nonce == s.nonce {
			// Finalize/recovery path: the nonce is beyond the diffs array
			// (empty or suffix-only after snapshot recovery). The SM's live
			// root is authoritative only for the current frozen nonce.
			var err error
			expected, err = s.sm.ComputeStateRoot()
			if err != nil {
				return fmt.Errorf("compute local state root: %w", err)
			}
		} else {
			// No stored root for a non-current nonce: we cannot reconstruct
			// the expected root, so reject rather than compare against the
			// wrong nonce's live root.
			return fmt.Errorf("%w: host %d at nonce %d: no post-state-root (session nonce %d)",
				types.ErrStateHashMismatch, hostIdx, resp.Nonce, s.nonce)
		}
		if !bytes.Equal(expected, resp.StateHash) {
			return fmt.Errorf("%w: host %d at nonce %d (local %x, host %x)",
				types.ErrStateHashMismatch, hostIdx, resp.Nonce, expected, resp.StateHash)
		}
	}

	// Verify and store state signature.
	if resp.StateSig != nil {
		expectedAddr := s.group[hostIdx].ValidatorAddress
		if err := s.verifyStateSignature(resp.Nonce, resp.StateHash, resp.StateSig, expectedAddr); err != nil {
			return fmt.Errorf("host %d: %w", hostIdx, err)
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

	s.noteContactLocked(hostIdx, s.nowLocked())
	s.noteOverlapLocked(resp)

	// Update sync nonce -- only advance, never regress.
	if resp.Nonce > s.hostSyncNonce[hostIdx] {
		s.hostSyncNonce[hostIdx] = resp.Nonce
	}

	// Queue mempool txs first so a stamped ConfirmStart wins over the
	// receipt-synthesized copy (same confirm:<id> dedup key).
	for _, tx := range resp.Mempool {
		s.addPendingFromHostLocked(hostIdx, resp, tx)
	}

	s.noteResponseClaimLocked(hostIdx, resp)

	// Queue receipt as MsgConfirmStart for the next diff if mempool did not
	// already carry it. Use inferenceNonce (the logical inference ID).
	if resp.Receipt != nil {
		s.addPendingFromHostLocked(hostIdx, resp, &types.DevshardTx{
			Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
				InferenceId:       inferenceNonce,
				ExecutorSig:       resp.Receipt,
				ConfirmedAt:       resp.ConfirmedAt,
				ObservedHeight:    resp.ObservedHeight,
				ObservedBlockHash: resp.ObservedBlockHash,
			}},
		})
	}

	// Gossiped mempool may carry finishes for other nonces; a timed-out record
	// is not treated as finished.
	for _, tx := range resp.Mempool {
		finish := tx.GetFinishInference()
		if finish == nil {
			continue
		}
		outcome, tracked := s.nonceStates[finish.InferenceId]
		if !tracked {
			continue
		}
		timedOut := false
		if s.sm != nil {
			if rec, ok := s.sm.Inference(finish.InferenceId); ok && rec.Status == types.StatusTimedOut {
				timedOut = true
			}
		}
		outcome.finished = !timedOut
	}

	if outcome, ok := s.nonceStates[inferenceNonce]; ok && resp.Receipt != nil && resp.ConfirmedAt > 0 {
		outcome.confirmedAt = resp.ConfirmedAt
	}

	s.noteGatewayTipLocked()
	return nil
}

// ProcessResponse updates session state from a host response. Thread-safe.
func (s *Session) ProcessResponse(hostIdx int, resp *host.HostResponse, inferenceNonce uint64) error {
	s.mu.Lock()
	err := s.processResponse(hostIdx, resp, inferenceNonce)
	s.mu.Unlock()
	s.publishHeightSyncView()
	return err
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

// composeDiffLocked builds, persists, commits, and returns a new diff
// (persist-first when a store is configured). extraTxs are appended after
// pending txs. Caller must hold s.mu.
//
// HA assumption: the gateway is the single-instance sequencer for this escrow,
// so this path does not race another writer on the same nonce. With a store,
// PreviewLocalBestEffort leaves the SM unchanged until AppendDiff succeeds,
// so persist failure cannot leave sequencer nonce/diffs ahead of durable state.
func (s *Session) composeDiffLocked(extraTxs []*types.DevshardTx) (types.Diff, int, error) {
	return s.composeDiffLockedInclude(extraTxs, nil)
}

func (s *Session) composeDiffLockedInclude(extraTxs []*types.DevshardTx, includePinned []uint64) (types.Diff, int, error) {
	nonce := s.nonce + 1
	hostIdx := int(nonce % uint64(len(s.group)))

	pending, held := s.splitPendingForComposeLocked(includePinned)
	sort.SliceStable(pending, func(i, j int) bool {
		return txPriority(pending[i]) < txPriority(pending[j])
	})

	candidates := make([]*types.DevshardTx, 0, len(pending)+len(extraTxs))
	candidates = append(candidates, pending...)
	candidates = append(candidates, extraTxs...)

	var diff types.Diff
	if s.store != nil {
		warmBefore := s.sm.WarmKeys()
		vd, err := s.sm.PreviewLocalBestEffort(nonce, candidates)
		if err != nil {
			return types.Diff{}, 0, fmt.Errorf("local apply: %w", err)
		}
		diff, err = s.signDiff(nonce, vd.Applied, vd.Root)
		if err != nil {
			return types.Diff{}, 0, err
		}
		delta := types.ComputeWarmKeyDelta(warmBefore, vd.WarmAfter)
		if err := s.persistDiffRetryLocked(types.DiffRecord{
			Diff:         diff,
			StateHash:    vd.Root,
			WarmKeyDelta: delta,
		}); err != nil {
			return types.Diff{}, 0, fmt.Errorf("persist diff: %w", err)
		}
		// Install the previewed post-state without a second apply (no re-sign,
		// so a retried compose cannot fork on a new UserSig after a durable
		// write). The gateway is the single sequencer and holds s.mu across
		// persist, so the nonce cannot have advanced concurrently.
		if !s.sm.CommitValidated(vd) {
			return types.Diff{}, 0, fmt.Errorf("commit diff nonce %d: state advanced concurrently", nonce)
		}
		s.diffs = append(s.diffs, diff)
		s.nonce = nonce
		s.retainPendingLocked(held, vd.Applied)
		s.maybeSaveSnapshotLocked()
		s.observeTurnLocked(diff)
		s.diffObserver(diff)
		return diff, hostIdx, nil
	}

	postStateRoot, applied, err := s.sm.ApplyLocalBestEffort(nonce, candidates)
	if err != nil {
		return types.Diff{}, 0, fmt.Errorf("local apply: %w", err)
	}
	diff, err = s.signDiff(nonce, applied, postStateRoot)
	if err != nil {
		return types.Diff{}, 0, err
	}
	s.diffs = append(s.diffs, diff)
	s.nonce = nonce
	s.retainPendingLocked(held, applied)
	s.observeTurnLocked(diff)
	s.diffObserver(diff)
	return diff, hostIdx, nil
}

func finishInferenceID(tx *types.DevshardTx) (uint64, bool) {
	if tx == nil {
		return 0, false
	}
	fi := tx.GetFinishInference()
	if fi == nil {
		return 0, false
	}
	return fi.InferenceId, true
}

func errorMissPendingID(tx *types.DevshardTx) (uint64, bool) {
	if id, ok := finishInferenceID(tx); ok {
		return id, true
	}
	if tx == nil {
		return 0, false
	}
	if to := tx.GetErrorMiss(); to != nil {
		return to.InferenceId, true
	}
	return 0, false
}

func (s *Session) pinPendingFinish(inferenceID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pinnedFinishIDs == nil {
		s.pinnedFinishIDs = make(map[uint64]int)
	}
	s.pinnedFinishIDs[inferenceID]++
}

// PinPendingFinish holds a pending MsgFinishInference out of composeDiff so a
// concurrent heartbeat or catch-up cannot apply it as a normal Finish before
// HandleErrorMiss. Safe to call when no Finish is pending.
func (s *Session) PinPendingFinish(inferenceID uint64) {
	s.pinPendingFinish(inferenceID)
}

// UnpinPendingFinish releases a pin taken with PinPendingFinish.
func (s *Session) UnpinPendingFinish(inferenceID uint64) {
	s.unpinPendingFinish(inferenceID)
}

func (s *Session) unpinPendingFinish(inferenceID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.pinnedFinishIDs[inferenceID] - 1
	if n <= 0 {
		delete(s.pinnedFinishIDs, inferenceID)
		return
	}
	s.pinnedFinishIDs[inferenceID] = n
}

func (s *Session) splitPendingForComposeLocked(includePinned []uint64) (candidates, held []*types.DevshardTx) {
	include := make(map[uint64]struct{}, len(includePinned))
	for _, id := range includePinned {
		include[id] = struct{}{}
	}
	for _, tx := range s.pendingTxs {
		if id, ok := errorMissPendingID(tx); ok && s.pinnedFinishIDs[id] > 0 {
			if _, ok := include[id]; !ok {
				held = append(held, tx)
				continue
			}
		}
		candidates = append(candidates, tx)
	}
	return candidates, held
}

// retainPendingLocked installs the txs compose held back and records the ones
// it actually applied. Candidates that were dropped by best-effort apply are
// deliberately not remembered: freeing their key lets an honest tx with the
// same tx_type:id be queued on a later response.
func (s *Session) retainPendingLocked(held, applied []*types.DevshardTx) {
	for _, tx := range applied {
		if key := devshardTxKey(tx); key != "" {
			s.appliedTxKeys[key] = struct{}{}
		}
	}
	if len(s.appliedTxKeys) > maxAppliedTxKeys {
		clear(s.appliedTxKeys)
	}
	s.pendingTxs = held
	clear(s.pendingTxKeys)
	for _, tx := range held {
		if key := devshardTxKey(tx); key != "" {
			s.pendingTxKeys[key] = struct{}{}
		}
	}
}

func (s *Session) observeTurnLocked(diff types.Diff) {
	if s.turnTracker == nil {
		return
	}
	hNow := heightsync.LogResidentHeight(diff.Txs, s.turnTracker.LastCompletedHeight())
	s.turnTracker.Observe(diff.Nonce, diff.Txs, hNow)

	// Any host-signed stamp in the log is a claim: an ack, which names its own
	// slot, or a stamped confirm / finish, which the executor rule attributes to
	// one. Crediting the log and not only the HTTP reply is what keeps a
	// recovered exchange — response dropped, leg arriving through a peer's
	// mempool — from still owing a heartbeat. Cadence asks whether the roster is
	// answering, not whether it agrees on a height, so sync_state is not
	// consulted; that is the rule TurnTracker uses for Q. A stampless leg is
	// still no round-trip to credit. Double-crediting an exchange that also came
	// back over HTTP is harmless: claimed is a set keyed by slot, cleared on Q.
	//
	// These txs are the applied set, so every signature here has already been
	// verified by the state machine.
	now := s.nowLocked()
	for _, tx := range diff.Txs {
		if ack := tx.GetHeightAck(); ack != nil {
			if !heightsync.StampPresent(ack.ObservedBlockHash) {
				continue
			}
			if s.heartbeat.NoteClaim(ack.SlotId, now) && s.anchors != nil && ack.ObservedHeight > 0 {
				s.anchors.RecordTurnover(ack.ObservedHeight)
			}
			s.noteAckObsLocked(ack)
			continue
		}
		inferenceID, height, ok := heightsync.ExecutorStamp(tx)
		if !ok {
			continue
		}
		slot, known := s.executorSlotLocked(inferenceID)
		if !known {
			continue
		}
		if s.heartbeat.NoteStamp(slot, now) && s.anchors != nil && height > 0 {
			s.anchors.RecordTurnover(height)
		}
	}
}

// executorSlotLocked is the state machine's executor rule, group[id % len(group)],
// which is what applyStartInference records as the record's ExecutorSlot and
// therefore the slot confirm and finish signatures are verified against.
func (s *Session) executorSlotLocked(inferenceID uint64) (uint32, bool) {
	if len(s.group) == 0 {
		return 0, false
	}
	return s.group[inferenceID%uint64(len(s.group))].SlotID, true
}

func (s *Session) nowLocked() time.Time {
	if s.clock == nil {
		return time.Now()
	}
	return s.clock()
}

// noteResponseClaimLocked credits a host-signed height claim to the host that
// just answered. An executor stamp on its receipt proves the same round-trip a
// heartbeat ack would, which is why a busy session owes no heartbeat.
func (s *Session) noteResponseClaimLocked(hostIdx int, resp *host.HostResponse) {
	if s.heartbeat == nil || resp == nil {
		return
	}
	if hostIdx < 0 || hostIdx >= len(s.group) {
		return
	}
	if resp.Receipt == nil || !heightsync.StampPresent(resp.ObservedBlockHash) {
		return
	}
	if s.heartbeat.NoteStamp(s.group[hostIdx].SlotID, s.nowLocked()) && s.anchors != nil && resp.ObservedHeight > 0 {
		s.anchors.RecordTurnover(resp.ObservedHeight)
	}
	s.noteContactLocked(hostIdx, s.nowLocked())
}

// persistDiffRetryLocked retries AppendDiff with backoff while holding s.mu.
// Unlocking during backoff would let a concurrent compose re-sign the same
// next nonce and risk ErrDiffFork after a durable write. Persist-first means
// sequencer state is not committed until this returns nil.
func (s *Session) persistDiffRetryLocked(rec types.DiffRecord) error {
	return storage.AppendDiffWithRetry(context.Background(), s.store, s.escrowID, rec)
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
	committedEntries := s.sm.ExportCommittedEntries()
	sealedNonces := s.sm.ExportSealedNonces()
	heightSyncFloor := s.sm.ExportHeightSyncFloor()
	cursor := make(map[int]uint64, len(s.hostSyncNonce))
	for k, v := range s.hostSyncNonce {
		cursor[k] = v
	}
	nonce := s.nonce
	store := s.store
	escrowID := s.escrowID

	go func() {
		defer s.snapshotInFlight.Store(false)
		writeSnapshot(store, escrowID, nonce, state, cursor, committedEntries, sealedNonces, heightSyncFloor)
	}()
}

// FlushSnapshot synchronously persists a state snapshot at the current nonce.
// It is called when a runtime is retired from memory (deactivate, settle,
// rotation) so the now-frozen escrow can later be rebuilt -- for a read-only
// debug/state request or a reactivation -- without replaying the diff tail
// accumulated since the last periodic (every snapshotInterval) snapshot.
//
// Periodic snapshots are only taken on snapshotInterval boundaries, so a
// retired escrow otherwise carries up to snapshotInterval-1 un-snapshotted
// diffs that every rebuild would replay. This flush captures the final nonce
// once, at the lifecycle transition, making all later rebuilds replay-free.
//
// It serializes against the async periodic writer via snapshotInFlight so a
// slow in-flight periodic save cannot land after (and overwrite) this final
// snapshot with a staler nonce. Safe to call once, at retire; a no-op when
// no diffs have been applied (nonce == 0) or no store is configured.
func (s *Session) FlushSnapshot() error {
	if s.store == nil {
		return nil
	}
	// Acquire the snapshot slot, waiting briefly for any in-flight async
	// save to finish. Bounded so retire never blocks indefinitely; if the
	// async writer is still busy after the wait we proceed anyway (SQLite
	// serializes the writes, and retire runs after drain so this is rare).
	acquired := false
	for i := 0; i < 200; i++ {
		if s.snapshotInFlight.CompareAndSwap(false, true) {
			acquired = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if acquired {
		defer s.snapshotInFlight.Store(false)
	}

	s.mu.Lock()
	if s.nonce == 0 {
		s.mu.Unlock()
		return nil
	}
	// Deep-copy state and cursor under the session lock, mirroring
	// maybeSaveSnapshotLocked, then release before the disk write.
	stateCopy := s.sm.ExportState()
	committedEntries := s.sm.ExportCommittedEntries()
	sealedNonces := s.sm.ExportSealedNonces()
	heightSyncFloor := s.sm.ExportHeightSyncFloor()
	cursor := make(map[int]uint64, len(s.hostSyncNonce))
	for k, v := range s.hostSyncNonce {
		cursor[k] = v
	}
	nonce := s.nonce
	store := s.store
	escrowID := s.escrowID
	s.mu.Unlock()

	return writeSnapshotErr(store, escrowID, nonce, stateCopy, cursor, committedEntries, sealedNonces, heightSyncFloor)
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
	defer func() {
		s.mu.Unlock()
		s.publishHeightSyncView()
	}()

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
	start := &types.MsgStartInference{
		InferenceId: nonce,
		Model:       params.Model,
		PromptHash:  promptHash,
		InputLength: params.InputLength,
		MaxTokens:   params.MaxTokens,
		StartedAt:   params.StartedAt,
	}
	if h, hash, ok := s.referenceStampLocked(nonce); ok {
		start.ObservedHeight = h
		start.ObservedBlockHash = hash
	}
	startTx := &types.DevshardTx{Tx: &types.DevshardTx_StartInference{
		StartInference: start,
	}}

	txsForDiff := []*types.DevshardTx{startTx}
	if params.ForceHeightSyncAnchor && s.heightSyncK != 0 {
		slots := s.heightSyncForceSlotsLocked()
		k := s.heightSyncK
		if k < slots {
			k = slots
		}
		if !s.sm.HeightSyncForcedTurnActive(nonce) {
			forceTx := &types.DevshardTx{Tx: &types.DevshardTx_ForceHeightSyncTurn{
				ForceHeightSyncTurn: &types.MsgForceHeightSyncTurn{
					TriggerNonce: nonce,
					EndNonce:     nonce + slots - 1,
					AnchorK:      k,
					SlotsNum:     slots,
					Reason:       "manual",
				},
			}}
			txsForDiff = []*types.DevshardTx{forceTx, startTx}
		}
	}

	diff, composedIdx, err := s.composeDiffLocked(txsForDiff)
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

// Payload is verified against the nonce's committed PromptHash, so a timeout raised on a nonce we
// never dispatched has to build it exactly as a real dispatch would.
func (p *PreparedInference) Payload() *host.InferencePayload { return p.params.Payload() }

// SendOnly sends a prepared inference to the host and returns the raw response
// without processing it. Use ProcessResponse separately to apply the response
// to session state. This split allows parallel network I/O with ordered processing.
func (s *Session) SendOnly(ctx context.Context, p *PreparedInference, stream io.Writer, receiptHandler func()) (*host.HostResponse, error) {
	legacyForce := p.params.ForceHeightSyncAnchor && s.heightSyncK == 0
	resp, err := s.clients[p.hostIdx].Send(ctx, host.HostRequest{
		Diffs:                 p.catchUp,
		Nonce:                 p.diff.Nonce,
		ForceHeightSyncAnchor: legacyForce,
		HeightSyncEscrow:      s.heightSyncEscrowHints(),
		Payload:               p.Payload(),
	}, stream, func(partial *host.HostResponse) {
		s.confirmStartOnReceipt(p.diff.Nonce, partial)
		if receiptHandler != nil {
			receiptHandler()
		}
	})
	if err != nil && state.IsPostStateRootMismatchError(err) {
		s.logStateRootMismatchUserDiagnostic(p)
	}
	return resp, err
}

// confirmStartOnReceipt queues the executor's receipt the moment it arrives rather than when the stream
// ends. A host that holds an empty stream open can outlast every other nonce on the escrow, and the
// receipt is only carried to the chain by the next diff: queued late, it can find no diff left to ride,
// and the nonce stays Pending forever, which is the one state its execution timeout cannot be voted in.
func (s *Session) confirmStartOnReceipt(inferenceNonce uint64, resp *host.HostResponse) {
	if resp == nil || len(resp.Receipt) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addPendingTx(&types.DevshardTx{
		Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
			InferenceId:       inferenceNonce,
			ExecutorSig:       resp.Receipt,
			ConfirmedAt:       resp.ConfirmedAt,
			ObservedHeight:    resp.ObservedHeight,
			ObservedBlockHash: resp.ObservedBlockHash,
		}},
	})
	// Both halves of the receipt move together: the chain learns the inference started, and this session
	// must vote the execution timeout that matches. Leaving confirmedAt behind makes it vote a refusal
	// against a record the chain already advanced, which every verifier then rejects.
	if outcome, tracked := s.nonceStates[inferenceNonce]; tracked && resp.ConfirmedAt > 0 {
		outcome.confirmedAt = resp.ConfirmedAt
	}
}

func (s *Session) heightSyncEscrowHints() *heightsync.EscrowHeightSyncHints {
	k, slots := s.heightSyncK, s.heightSyncSlots
	if k == 0 {
		k = 10
	}
	if slots == 0 {
		slots = uint64(len(s.group))
	}
	return s.sm.HeightSyncEscrowHints(k, slots)
}

func (s *Session) logStateRootMismatchUserDiagnostic(p *PreparedInference) {
	if p == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sm.LogStateRootMismatchDiagnostic(state.StateRootMismatchOpts{
		Side:          "devshardctl",
		Nonce:         p.diff.Nonce,
		DiffPostState: p.diff.PostStateRoot,
		SealClock:     s.sm.AutoSealStateClock(),
	})
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
	err = s.processResponse(p.hostIdx, resp, p.diff.Nonce)
	s.mu.Unlock()
	s.publishHeightSyncView()
	if err != nil {
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

	resp, err := s.clients[hostIdx].Send(ctx, host.HostRequest{Diffs: catchUp, Nonce: diff.Nonce, HeightSyncEscrow: s.heightSyncEscrowHints()}, nil, nil)
	if err != nil {
		logging.Warn("sendDiffRound host dead", "subsystem", "finalize", "escrow", s.escrowID,
			"nonce", diff.Nonce, "host", hostIdx, "error", err)
		return nil // dead host, not fatal
	}

	logging.Info("sendDiffRound response", "subsystem", "finalize", "escrow", s.escrowID,
		"nonce", diff.Nonce, "host", hostIdx,
		"resp_nonce", resp.Nonce, "has_sig", resp.StateSig != nil)

	s.mu.Lock()
	err = s.processResponse(hostIdx, resp, diff.Nonce)
	if err == nil {
		s.logSignatureProgress(resp.Nonce)
	}
	s.mu.Unlock()
	s.publishHeightSyncView()
	return err
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

// sendCatchUp sends existing diffs to a host, admission-free: they are already signed by the group.
func (s *Session) sendCatchUp(ctx context.Context, hostIdx int) error {
	return s.sendCatchUpWith(ctx, hostIdx, s.getFinalizeClients()[hostIdx])
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
			logging.Warn("sendCatchUp chunk failed", "subsystem", "finalize", "escrow", s.escrowID,
				"nonce", nonce, "host", hostIdx,
				"chunk", chunkNum, "error", err)
			return fmt.Errorf("catch-up chunk %d to host %d: %w", chunkNum, hostIdx, err)
		}

		logging.Info("sendCatchUp chunk response", "subsystem", "finalize", "escrow", s.escrowID,
			"nonce", nonce, "host", hostIdx,
			"chunk", chunkNum,
			"resp_nonce", resp.Nonce, "has_sig", resp.StateSig != nil)

		s.mu.Lock()
		err = s.processResponse(hostIdx, resp, chunkNonce)
		if err == nil {
			s.logSignatureProgress(resp.Nonce)
		}
		s.mu.Unlock()
		if err != nil {
			s.publishHeightSyncView()
			return err
		}

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

	s.publishHeightSyncView()
	return nil
}

type physicalHost struct {
	idx  int
	addr string
}

func (s *Session) uniquePhysicalHosts() []physicalHost {
	n := len(s.group)
	seen := make(map[string]bool)
	hosts := make([]physicalHost, 0, n)
	for i := 0; i < n; i++ {
		addr := s.group[i].ValidatorAddress
		if seen[addr] {
			continue
		}
		seen[addr] = true
		hosts = append(hosts, physicalHost{idx: i, addr: addr})
	}
	return hosts
}

// CatchUpAllHosts gives every unique physical host the diffs it has not seen. A host the gateway never
// dispatched to still ends up holding the escrow, so it can answer a validation or a timeout vote
// instead of reporting the session as unknown. It spends no nonce: it replays diffs that already exist.
func (s *Session) CatchUpAllHosts(ctx context.Context) error {
	if phase := s.sm.Phase(); phase != types.PhaseActive {
		return fmt.Errorf("catch up all hosts: session phase %d, want active", phase)
	}
	hosts := s.uniquePhysicalHosts()
	// Hosts are independent, so the group waits for the slowest one instead of the sum of them.
	finalizeClients := s.getFinalizeClients()
	perHost := make([]error, len(hosts))
	var wg sync.WaitGroup
	for i, target := range hosts {
		wg.Go(func() {
			if err := s.sendCatchUpWith(ctx, target.idx, finalizeClients[target.idx]); err != nil {
				perHost[i] = fmt.Errorf("host %d: %w", target.idx, err)
			}
		})
	}
	wg.Wait()

	failures := slices.DeleteFunc(perHost, func(err error) bool { return err == nil })
	logging.Info("catch up all hosts", "subsystem", "sync", "escrow", s.escrowID,
		"nonce", s.Nonce(), "unique_hosts", len(hosts), "failed", len(failures))
	return errors.Join(failures...)
}

// SyncHosts propagates signed diffs to every unique physical host and drains
// host-proposed mempool txs (validations, finishes) into new diffs. This is
// finalize Phase B-style catch-up without entering PhaseFinalizing — use before
// observability checks when validators on join nodes may be ahead of genesis.
func (s *Session) SyncHosts(ctx context.Context) error {
	if s.sm.Phase() != types.PhaseActive {
		return fmt.Errorf("sync hosts: session phase %d, want active", s.sm.Phase())
	}

	hosts := s.uniquePhysicalHosts()
	startNonce := s.Nonce()
	logging.Info("sync hosts started", "subsystem", "sync", "escrow", s.escrowID,
		"nonce", startNonce, "unique_hosts", len(hosts))

	var failures []error
	const syncCycles = 2
	for cycle := 0; cycle < syncCycles; cycle++ {
		for _, h := range hosts {
			if err := s.sendCatchUp(ctx, h.idx); err != nil {
				failures = append(failures, fmt.Errorf("cycle %d host %d: %w", cycle+1, h.idx, err))
			}
		}
		for i := 0; i < len(s.group); i++ {
			s.mu.Lock()
			hasPending := len(s.pendingTxs) > 0
			s.mu.Unlock()
			if !hasPending {
				break
			}
			if err := s.sendDiffRound(ctx, nil); err != nil {
				return fmt.Errorf("sync hosts cycle %d diff round: %w", cycle+1, err)
			}
		}
	}

	for _, h := range hosts {
		if err := s.sendCatchUp(ctx, h.idx); err != nil {
			failures = append(failures, fmt.Errorf("final host %d: %w", h.idx, err))
		}
	}

	logging.Info("sync hosts complete", "subsystem", "sync", "escrow", s.escrowID,
		"start_nonce", startNonce, "end_nonce", s.Nonce(), "failed", len(failures))
	return errors.Join(failures...)
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
// Returns "" when tx is nil, the inner message is nil, or the type is not
// deduped (user/sequencer types and unknown txs). Dedup identity only; whether
// a host may enqueue a type is hostMayProposeTx.
func devshardTxKey(tx *types.DevshardTx) string {
	if tx == nil {
		return ""
	}
	switch inner := tx.GetTx().(type) {
	case *types.DevshardTx_FinishInference:
		if inner.FinishInference == nil {
			return ""
		}
		return fmt.Sprintf("finish:%d", inner.FinishInference.InferenceId)
	case *types.DevshardTx_ConfirmStart:
		if inner.ConfirmStart == nil {
			return ""
		}
		return fmt.Sprintf("confirm:%d", inner.ConfirmStart.InferenceId)
	case *types.DevshardTx_Validation:
		if inner.Validation == nil {
			return ""
		}
		return fmt.Sprintf("validation:%d:%d", inner.Validation.InferenceId, inner.Validation.ValidatorSlot)
	case *types.DevshardTx_ValidationVote:
		if inner.ValidationVote == nil {
			return ""
		}
		return fmt.Sprintf("vote:%d:%d", inner.ValidationVote.InferenceId, inner.ValidationVote.VoterSlot)
	case *types.DevshardTx_RevealSeed:
		if inner.RevealSeed == nil {
			return ""
		}
		return fmt.Sprintf("reveal_seed:%d", inner.RevealSeed.SlotId)
	case *types.DevshardTx_HeightAck:
		if inner.HeightAck == nil {
			return ""
		}
		return fmt.Sprintf("height_ack:%d:%d", inner.HeightAck.RefNonce, inner.HeightAck.SlotId)
	default:
		return ""
	}
}

// hostMayProposeTx is the response-boundary allowlist. It is independent of
// devshardTxKey: adding a dedup key for Start must not start enqueueing Starts.
// Nil inner messages are rejected so a oneof with a nil payload cannot panic
// later or sneak past the allowlist.
func hostMayProposeTx(tx *types.DevshardTx) bool {
	if tx == nil {
		return false
	}
	switch inner := tx.GetTx().(type) {
	case *types.DevshardTx_FinishInference:
		return inner.FinishInference != nil
	case *types.DevshardTx_ConfirmStart:
		return inner.ConfirmStart != nil
	case *types.DevshardTx_Validation:
		return inner.Validation != nil
	case *types.DevshardTx_ValidationVote:
		return inner.ValidationVote != nil
	case *types.DevshardTx_RevealSeed:
		return inner.RevealSeed != nil
	case *types.DevshardTx_HeightAck:
		return inner.HeightAck != nil
	default:
		return false
	}
}

// hostRecoveryDedupKey is the CollectTimeoutVotes / CollectErrorMissVotes
// identity for mempool copies. User-proposed types never become recovery.
func hostRecoveryDedupKey(tx *types.DevshardTx) string {
	if !hostMayProposeTx(tx) {
		return ""
	}
	return devshardTxKey(tx)
}

// addPendingTx appends tx to pendingTxs unless its key is already queued or
// already landed in a diff.
func (s *Session) addPendingTx(tx *types.DevshardTx) {
	if tx == nil {
		return
	}
	key := devshardTxKey(tx)
	if key != "" {
		if s.txKeyQueuedOrApplied(key) {
			return
		}
		s.pendingTxKeys[key] = struct{}{}
	}
	s.pendingTxs = append(s.pendingTxs, tx)
}

func (s *Session) txKeyQueuedOrApplied(key string) bool {
	if _, dup := s.pendingTxKeys[key]; dup {
		return true
	}
	_, applied := s.appliedTxKeys[key]
	return applied
}

// hostTxInferenceID returns the inference a host-proposed tx acts on. HeightAck
// and RevealSeed are slot-scoped rather than inference-scoped.
func hostTxInferenceID(tx *types.DevshardTx) (uint64, bool) {
	switch {
	case tx.GetConfirmStart() != nil:
		return tx.GetConfirmStart().InferenceId, true
	case tx.GetFinishInference() != nil:
		return tx.GetFinishInference().InferenceId, true
	case tx.GetValidation() != nil:
		return tx.GetValidation().InferenceId, true
	case tx.GetValidationVote() != nil:
		return tx.GetValidationVote().InferenceId, true
	default:
		return 0, false
	}
}

// rejectUnverifiedHostTx returns a non-nil error when local state can already
// prove tx cannot apply. Two checks, both map lookups on the hot path:
//
// The inference must be live. applyConfirmStart, applyValidation and
// applyValidationVote all reject an id that is absent or sealed, so admitting
// one only buys an expensive signature recovery at compose time. Rejecting
// here also stops a host from parking unbounded made-up ids in pending, which
// matters now that a tx failing to apply releases its dedup key.
//
// A Finish must additionally come from the assigned executor. Confirm and
// validation still authenticate on apply; Finish is checked here so an
// unsigned or wrong-slot one cannot sit in pending and ride into a later Diff.
// That signature check is deliberately resolver-free: a Finish signed by a warm
// key for a slot with no cached binding is undecidable without a bridge round
// trip, so it is allowed through and authenticated on apply. Blocking a host
// response on a chain query would stall every inference on the session.
func (s *Session) rejectUnverifiedHostTx(tx *types.DevshardTx) error {
	inferenceID, scoped := hostTxInferenceID(tx)
	if !scoped {
		return nil
	}
	if s.sm == nil {
		// Nothing to authenticate against, so admit nothing: an inference-scoped
		// tx is only admissible when a live record backs it.
		return types.ErrInferenceNotFound
	}
	executorSlot, ok := s.sm.InferenceExecutorSlot(inferenceID)
	if !ok {
		return types.ErrInferenceNotFound
	}
	fi := tx.GetFinishInference()
	if fi == nil {
		return nil
	}
	if fi.ExecutorSlot != executorSlot {
		return types.ErrWrongExecutorSlot
	}
	if fi.EscrowId != s.escrowID {
		return types.ErrEscrowIDMismatch
	}
	return s.sm.RejectFinishProposerSigLocal(fi)
}

// recoveryHostIdx marks a tx recovered from a timeout vote rather than read off
// one host's response, so there is no response envelope or owning slot set.
const recoveryHostIdx = -1

// addPendingFromHostLocked queues a mempool/receipt tx from one host response
// or timeout-recovery copy. User/sequencer types are dropped. Finish must be
// signed by the assigned executor of a live inference. A host-signed raise
// that does not match this hop's response-leg envelope is dropped (spec §14
// rule 2). In-process, recovery, and omit-section hops leave HasEnvelope unset
// and skip the envelope check.
func (s *Session) addPendingFromHostLocked(hostIdx int, resp *host.HostResponse, tx *types.DevshardTx) {
	if tx == nil {
		return
	}
	if !hostMayProposeTx(tx) {
		logging.Warn("dropped user-proposed tx from host mempool",
			"subsystem", "session", "escrow", s.escrowID, "host_idx", hostIdx,
			"tx_type", fmt.Sprintf("%T", tx.GetTx()))
		return
	}
	// Settle dedup before verifying: every host gossips the same Finish, and
	// signature recovery is far more expensive than the map lookup that would
	// discard the result anyway. The envelope filter below can swap tx for a
	// stamped copy, but never changes its dedup key.
	if key := devshardTxKey(tx); key != "" && s.txKeyQueuedOrApplied(key) {
		return
	}
	if err := s.rejectUnverifiedHostTx(tx); err != nil {
		// A tx for an inference the sequencer no longer tracks is ordinary
		// late gossip after auto-seal, not evidence of a misbehaving host.
		if errors.Is(err, types.ErrInferenceNotFound) {
			logging.Debug("dropped host tx for unknown or sealed inference",
				"subsystem", "session", "escrow", s.escrowID, "host_idx", hostIdx,
				"tx_type", fmt.Sprintf("%T", tx.GetTx()))
		} else {
			logging.Warn("dropped host Finish not signed by inference executor",
				"subsystem", "session", "escrow", s.escrowID, "host_idx", hostIdx,
				"error", err)
		}
		return
	}
	if resp != nil && resp.HasEnvelope {
		floor, _, _ := s.sm.HeightSyncFloorAsOf(s.nonce + 1)
		kept, n := heightsync.FilterHostRaises(floor, resp.EnvelopeHeight, true, s.ownSlotsLocked(hostIdx), []*types.DevshardTx{tx})
		if n > 0 {
			logging.Warn("omitted host-signed raise: stamp does not match response envelope",
				"subsystem", "heightsync", "escrow", s.escrowID, "host_idx", hostIdx,
				"floor", floor, "envelope", resp.EnvelopeHeight)
			return
		}
		if len(kept) == 0 {
			return
		}
		tx = kept[0]
	}
	s.addPendingTx(tx)
}

func (s *Session) ownSlotsLocked(hostIdx int) map[uint32]struct{} {
	if hostIdx < 0 || hostIdx >= len(s.group) {
		return nil
	}
	slots := s.addrToSlots[s.group[hostIdx].ValidatorAddress]
	out := make(map[uint32]struct{}, len(slots))
	for _, sl := range slots {
		out[sl] = struct{}{}
	}
	return out
}

const maxAppliedTxKeys = 100_000

// clearPendingTxs drops the pending tx slice and the keys that were reserving
// a slot in it. Keys of txs already applied in earlier diffs are preserved, so
// another host's mempool copy is still not re-added. The applied set is
// bulk-cleared only when it exceeds the cap.
func (s *Session) clearPendingTxs() {
	s.pendingTxs = nil
	clear(s.pendingTxKeys)
	if len(s.appliedTxKeys) > maxAppliedTxKeys {
		clear(s.appliedTxKeys)
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

// FinishTxFor marshals the pending MsgFinishInference for inferenceID under
// the session lock so callers never iterate pendingTxs while composeDiffLocked
// sorts it in place.
func (s *Session) FinishTxFor(inferenceID uint64) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return MarshalFinishTx(s.pendingTxs, inferenceID)
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

// HasQuorumAt reports whether in-memory signatures at nonce meet the session
// quorum threshold. Used by settle to decide whether Finalize must re-run
// after a snapshot-only recovery left PhaseSettlement but empty signatures.
func (s *Session) HasQuorumAt(nonce uint64) bool {
	return s.hasQuorum(nonce, s.sm.QuorumThreshold())
}

// forgetHostState rewinds a host that reports it does not hold the escrow. Catch-up sends only what
// follows the host's cursor, so a host that lost its storage is never taught again while the cursor
// still claims it is current: the rewind is the only way back.
func (s *Session) forgetHostState(hostIdx int, err error) {
	if !transport.IsSessionNotFound(err) {
		return
	}
	s.RewindHostCatchUp(hostIdx, "host lost the escrow")
}

// RewindHostCatchUp rewinds a host to the start of the history we still hold, so the next request
// carries the whole chain instead of the tail its cursor claims it needs. Reports whether it moved.
func (s *Session) RewindHostCatchUp(hostIdx int, cause string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cursor, tracked := s.hostSyncNonce[hostIdx]
	if hostIdx < 0 || hostIdx >= len(s.group) || !tracked || cursor == 0 {
		return false
	}
	// Only as far back as the diffs actually held: after a restart the history starts at the group's
	// lowest cursor, and rewinding past it would hand the host a chain missing its own beginning.
	var earliest uint64
	if len(s.diffs) > 0 {
		earliest = s.diffs[0].Nonce - 1
	}
	if cursor <= earliest {
		return false
	}
	logging.Warn("rewinding a host's catch-up cursor", "subsystem", "session", "cause", cause,
		"escrow", s.escrowID, "host", hostIdx, "cursor", cursor, "rewound_to", earliest)
	s.hostSyncNonce[hostIdx] = earliest
	return true
}

// getFinalizeClients mirrors s.clients with admission control stripped, for the protocol paths that must
// reach a host the participant budget would refuse. Rebuilt per call: s.clients is small, both callers
// are network-bound, and a cache would have to be invalidated by anything that swaps a client.
func (s *Session) getFinalizeClients() []HostClient {
	type admissionBypasser interface {
		WithoutAdmission() any
	}
	clients := make([]HostClient, len(s.clients))
	for i, c := range s.clients {
		clients[i] = c
		if b, ok := c.(admissionBypasser); ok {
			if hc, ok := b.WithoutAdmission().(HostClient); ok {
				clients[i] = hc
			}
		}
	}
	return clients
}

// fetchSignature tries to retrieve an already-stored signature from the host
// via the SignatureFetcher interface (GET /signatures?nonce=N). This avoids
// sending diffs when the host already signed the state via gossip.
// Returns true if a signature was successfully fetched and stored.
// verifyStateSignature recovers the signer of signature over the canonical
// StateSignatureContent{postRoot, escrowID, nonce} preimage and returns an error
// unless it recovers to expectedAddr (with warm-key fallback). Shared by
// processResponse (inbound host responses) and fetchSignature (pulled signatures)
// so a host cannot get bytes that don't verify against its slot into the pool.
func (s *Session) verifyStateSignature(nonce uint64, postRoot, signature []byte, expectedAddr string) error {
	sigData, err := proto.Marshal(&types.StateSignatureContent{
		StateRoot: postRoot,
		EscrowId:  s.escrowID,
		Nonce:     nonce,
	})
	if err != nil {
		return fmt.Errorf("marshal state sig content: %w", err)
	}
	recovered, err := s.verifier.RecoverAddress(sigData, signature)
	if err != nil {
		return fmt.Errorf("%w: %v", types.ErrInvalidStateSig, err)
	}
	if recovered != expectedAddr && !s.sm.CheckWarmKey(recovered, expectedAddr) {
		return fmt.Errorf("%w: expected %s, got %s", types.ErrInvalidStateSig, expectedAddr, recovered)
	}
	return nil
}

func (s *Session) verifyTimeoutVote(inferenceID uint64, reason types.TimeoutReason, vote *types.TimeoutVote, expectedAddr string) error {
	voteData, err := proto.Marshal(&types.TimeoutVoteContent{
		EscrowId:    s.escrowID,
		InferenceId: inferenceID,
		Reason:      reason,
		Accept:      vote.Accept,
	})
	if err != nil {
		return fmt.Errorf("marshal timeout vote content: %w", err)
	}
	recovered, err := s.verifier.RecoverAddress(voteData, vote.Signature)
	if err != nil {
		return fmt.Errorf("%w: %v", types.ErrInvalidVoteSig, err)
	}
	if recovered != expectedAddr &&
		s.sm.WarmKeys()[vote.VoterSlot] != recovered &&
		!s.sm.CheckWarmKey(recovered, expectedAddr) {
		return fmt.Errorf("%w: expected %s, got %s", types.ErrInvalidVoteSig, expectedAddr, recovered)
	}
	if owner := s.sm.SlotAddress(vote.VoterSlot); owner != expectedAddr {
		return fmt.Errorf("%w: slot %d is owned by %s, not by responder %s",
			types.ErrInvalidVoteSig, vote.VoterSlot, owner, expectedAddr)
	}
	return nil
}

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

	postRoot, ok := s.postStateRootForNonce(nonce)
	if !ok {
		// Snapshot-only recovery can leave s.diffs empty while the SM is
		// already restored to the frozen final state. Mirror processResponse:
		// the SM's live root is authoritative ONLY for the current (frozen)
		// nonce. For any other nonce we have no way to reconstruct the root,
		// so refuse rather than verify against a root for a different nonce.
		if nonce != s.nonce {
			logging.Info("fetchSignature: no post-state-root for nonce", "subsystem", "finalize",
				"escrow", s.escrowID, "nonce", nonce, "host", hostIdx, "session_nonce", s.nonce)
			return false
		}
		var err error
		postRoot, err = s.sm.ComputeStateRoot()
		if err != nil {
			logging.Info("fetchSignature: no post-state-root for nonce", "subsystem", "finalize",
				"escrow", s.escrowID, "nonce", nonce, "host", hostIdx, "error", err)
			return false
		}
		logging.Info("fetchSignature: using live state root", "subsystem", "finalize",
			"escrow", s.escrowID, "nonce", nonce, "host", hostIdx)
	}

	for slotID := range sigs {
		addr := s.sm.SlotAddress(slotID)
		if addr != expectedAddr {
			continue
		}
		if err := s.verifyStateSignature(nonce, postRoot, sigs[slotID], expectedAddr); err != nil {
			logging.Warn("fetchSignature: rejected unverified signature", "subsystem", "finalize",
				"escrow", s.escrowID, "nonce", nonce, "host", hostIdx, "slot", slotID, "error", err)
			return false
		}
		if _, ok := s.signatures[nonce]; !ok {
			s.signatures[nonce] = make(map[uint32][]byte)
		}
		for _, slot := range s.addrToSlots[expectedAddr] {
			s.signatures[nonce][slot] = sigs[slotID]
			if s.store != nil {
				if sigErr := s.store.AddSignature(s.escrowID, nonce, slot, sigs[slotID]); sigErr != nil {
					logging.Warn("failed to persist fetched signature",
						"escrow_id", s.escrowID, "nonce", nonce, "slot", slot, "error", sigErr)
				}
			}
		}
		logging.Info("fetched existing signature", "subsystem", "finalize", "escrow", s.escrowID,
			"nonce", nonce, "host", hostIdx, "address", expectedAddr)
		s.logSignatureProgress(nonce)
		return true
	}
	logging.Info("fetchSignature: no signature from expected host", "subsystem", "finalize",
		"escrow", s.escrowID, "nonce", nonce, "host", hostIdx)
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

func timeoutInferenceTx(inferenceID uint64, reason types.TimeoutReason, votes []*types.TimeoutVote) *types.DevshardTx {
	return &types.DevshardTx{
		Tx: &types.DevshardTx_TimeoutInference{TimeoutInference: &types.MsgTimeoutInference{
			InferenceId: inferenceID,
			Reason:      reason,
			Votes:       votes,
		}},
	}
}

func errorMissTx(inferenceID uint64, votes []*types.ErrorMissVote) *types.DevshardTx {
	return &types.DevshardTx{
		Tx: &types.DevshardTx_ErrorMiss{ErrorMiss: &types.MsgErrorMiss{
			InferenceId: inferenceID,
			Votes:       votes,
		}},
	}
}

// AddPendingTimeoutTx adds a MsgTimeoutInference to the pending tx queue.
func (s *Session) AddPendingTimeoutTx(inferenceID uint64, reason types.TimeoutReason, votes []*types.TimeoutVote) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addPendingTx(timeoutInferenceTx(inferenceID, reason, votes))
}

// SendPendingDiff creates a diff from pending txs (no new MsgStartInference),
// applies it locally, and submits the signed diff to a reachable host. Used for
// timeout submission and draining host-proposed transactions.
func (s *Session) SendPendingDiff(ctx context.Context) error {
	_, err := s.sendPendingDiff(ctx, nil, nil)
	return err
}

func (s *Session) sendPendingDiff(ctx context.Context, extraTxs []*types.DevshardTx, includePinned []uint64) (types.Diff, error) {
	s.mu.Lock()
	diff, hostIdx, err := s.composeDiffLockedInclude(extraTxs, includePinned)
	if err != nil {
		s.mu.Unlock()
		return types.Diff{}, err
	}
	s.mu.Unlock()

	finalizeClients := s.getFinalizeClients()
	var lastErr error
	for offset := 0; offset < len(finalizeClients); offset++ {
		candidateIdx := (hostIdx + offset) % len(finalizeClients)

		s.mu.Lock()
		catchUp := s.diffsForHost(candidateIdx)
		s.mu.Unlock()

		// Admission-free, like signature collection: this diff carries a vote the verifiers already cast, so
		// the participant budget refusing it throws that work away and costs the host nothing.
		resp, sendErr := finalizeClients[candidateIdx].Send(ctx, host.HostRequest{
			Diffs: catchUp,
			Nonce: diff.Nonce,
		}, nil, nil)
		if sendErr != nil {
			lastErr = fmt.Errorf("send pending diff to host %d: %w", candidateIdx, sendErr)
			if ctx.Err() != nil {
				return diff, lastErr
			}
			continue
		}

		s.mu.Lock()
		processErr := s.processResponse(candidateIdx, resp, diff.Nonce)
		s.mu.Unlock()
		s.publishHeightSyncView()
		if processErr != nil {
			return diff, fmt.Errorf("process pending diff response from host %d: %w", candidateIdx, processErr)
		}
		return diff, nil
	}

	return diff, fmt.Errorf("send pending diff nonce %d to any host: %w", diff.Nonce, lastErr)
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

func (s *Session) SetDiffObserver(observer func(types.Diff)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if observer == nil {
		observer = func(types.Diff) {}
	}
	s.diffObserver = observer
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
	reasonLabel, deadline := s.TimeoutDeadline(nonce, sendTime)
	s.mu.Lock()
	hostIdx := int(nonce % uint64(len(s.group)))
	hostID := s.HostLabel(hostIdx)
	s.mu.Unlock()

	logFields := func(extra ...any) []any {
		base := []any{"escrow", s.escrowID, "nonce", nonce, "host", hostID}
		return append(base, extra...)
	}

	// A canceled wait leaves Reason empty, because Reason means the deadline was
	// reached and callers count an inference timeout from it. Outcome and
	// DetailReason still report the skip.
	var reason types.TimeoutReason
	if reasonLabel == "execution" {
		// A receipt moves the inference to Started. Publish its pending
		// ConfirmStart before asking peers to verify an execution timeout so
		// they evaluate the same receipt-backed state.
		st := s.sm.SnapshotState()
		if rec, ok := st.Inferences[nonce]; !ok {
			return TimeoutResult{}, fmt.Errorf("execution timeout inference %d not found", nonce)
		} else if rec.Status == types.StatusPending {
			if err := s.SendPendingDiff(ctx); err != nil {
				return TimeoutResult{}, fmt.Errorf("publish receipt before execution timeout: %w", err)
			}
		}

		if !sleepUntilDeadlineWithHeartbeat(ctx, deadline, func() {
			logging.Stage(ctx, "timeout_waiting", logFields("reason", "execution", "remaining_ms", time.Until(deadline).Milliseconds())...)
		}) {
			return TimeoutResult{Outcome: "skipped", DetailReason: "context_canceled"}, ctx.Err()
		}
		reason = types.TimeoutReason_TIMEOUT_REASON_EXECUTION
	} else {
		if !sleepUntilDeadlineWithHeartbeat(ctx, deadline, func() {
			logging.Stage(ctx, "timeout_waiting", logFields("reason", "refused", "remaining_ms", time.Until(deadline).Milliseconds())...)
		}) {
			return TimeoutResult{Outcome: "skipped", DetailReason: "context_canceled"}, ctx.Err()
		}
		reason = types.TimeoutReason_TIMEOUT_REASON_REFUSED
	}

	result := TimeoutResult{Reason: timeoutReasonLogLabel(reason)}

	if elapsed, refusalTimeout, unreachable := s.refusalDeadlineUnreachable(reason, payload); unreachable {
		logging.Stage(ctx, "timeout_skipped", logFields("reason", "refusal_deadline_unreachable",
			"elapsed_seconds", elapsed, "refusal_timeout_seconds", refusalTimeout)...)
		result.Outcome = "skipped"
		result.DetailReason = "refusal_deadline_unreachable"
		return result, fmt.Errorf("inference %d: refusal deadline unreachable", nonce)
	}

	logging.Stage(ctx, "timeout_started", logFields("reason", result.Reason)...)

	if reason == types.TimeoutReason_TIMEOUT_REASON_EXECUTION {
		s.mu.Lock()
		hasPendingFinish := HasMsgFinish(s.pendingTxs, nonce)
		s.mu.Unlock()
		if hasPendingFinish {
			if err := s.SendPendingDiff(ctx); err != nil {
				logging.Stage(ctx, "timeout_recovery_send_failed", logFields("reason", result.Reason, "error", err)...)
				return result, fmt.Errorf("publish pending finish before execution timeout: %w", err)
			}
			logging.Stage(ctx, "timeout_recovery_published", logFields("reason", result.Reason)...)
			return result, nil
		}
	}

	verifiers := s.TimeoutVerifiers()

	votes, recovery, _, verifierError, err := s.collectTimeoutVotes(ctx, nonce, reason, payload, verifiers, nil)
	if err != nil {
		result.Outcome = "vote_collection_failed"
		if ctx.Err() != nil {
			result.DetailReason = "context_canceled"
		}
		return result, fmt.Errorf("collect timeout votes: %w", err)
	}

	result.Votes = len(votes)
	result.Accepted = s.HasSufficientTimeoutVotes(votes)
	if result.Accepted {
		s.AddPendingTimeoutTx(nonce, reason, votes)
		diff, err := s.sendPendingDiff(ctx, nil, nil)
		result.Applied = HasMsgTimeout(diff.Txs, nonce)
		if !result.Applied {
			if record, found := s.sm.GetInference(nonce); found && record.Status == types.StatusTimedOut {
				result.Applied = true
			}
		}
		if err != nil {
			result.DetailReason = "timeout_diff_delivery_failed"
			if result.Applied {
				result.Outcome = "applied"
			} else {
				result.Outcome = "diff_send_failed"
			}
			logging.Stage(ctx, "timeout_diff_send_failed", logFields("reason", result.Reason, "error", err)...)
			return result, fmt.Errorf("send timeout diff: %w", err)
		}
		if result.Applied {
			result.Outcome = "applied"
		} else {
			// The send succeeded but the transaction did not land. Report it
			// rather than counting an applied timeout; the caller's control flow
			// is unchanged.
			result.Outcome = "diff_send_failed"
			result.DetailReason = "timeout_not_applied"
		}
		logging.Stage(ctx, "timeout_completed", logFields("reason", result.Reason)...)
		return result, fmt.Errorf("inference %d timed out: %s", nonce, reason)
	}

	recovery = host.RecoveryTxsFor(recovery, nonce)
	if reason == types.TimeoutReason_TIMEOUT_REASON_REFUSED && len(recovery) > 0 {
		s.mu.Lock()
		for _, tx := range recovery {
			s.addPendingFromHostLocked(recoveryHostIdx, nil, tx)
		}
		s.mu.Unlock()
		if err := s.SendPendingDiff(ctx); err != nil {
			logging.Stage(ctx, "timeout_recovery_send_failed", logFields("reason", result.Reason, "error", err)...)
			return result, fmt.Errorf("publish timeout recovery: %w", err)
		}
		logging.Stage(ctx, "timeout_recovery_published", logFields("reason", result.Reason)...)
		return result, nil
	}

	if verifierError != "" {
		result.Outcome = "vote_collection_failed"
		result.DetailReason = verifierError
		if ctx.Err() != nil {
			result.DetailReason = "context_canceled"
		}
	} else {
		result.Outcome = "insufficient_votes"
		result.DetailReason = "vote_weight_short"
	}
	logging.Stage(ctx, "timeout_insufficient_votes", logFields("reason", result.Reason)...)
	return result, fmt.Errorf("inference %d timed out but insufficient votes", nonce)
}

// refusalDeadlineUnreachable reports whether a refusal vote is already lost. A verifier measures the
// deadline as its own clock in seconds minus the record's StartedAt, so a stamp in the future — or in
// the wrong unit — makes every vote a guaranteed reject, and the round is pure waste.
func (s *Session) refusalDeadlineUnreachable(reason types.TimeoutReason, payload *host.InferencePayload) (elapsed, refusalTimeout int64, unreachable bool) {
	if reason != types.TimeoutReason_TIMEOUT_REASON_REFUSED || payload == nil {
		return 0, 0, false
	}
	refusalTimeout = s.sm.Config().RefusalTimeout
	elapsed = time.Now().Unix() - payload.StartedAt
	return elapsed, refusalTimeout, elapsed < refusalTimeout
}

func (s *Session) TimeoutDeadline(nonce uint64, sendTime time.Time) (string, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := s.sm.Config()
	if outcome := s.nonceStates[nonce]; outcome != nil && outcome.confirmedAt > 0 {
		return "execution", time.Unix(outcome.confirmedAt, 0).Add(
			time.Duration(cfg.ExecutionTimeout)*time.Second + TimeoutBuffer,
		)
	}
	return "refused", sendTime.Add(
		time.Duration(cfg.RefusalTimeout)*time.Second + TimeoutBuffer,
	)
}

func (s *Session) HandleErrorMiss(ctx context.Context, nonce uint64, finishTx, responsePayload []byte) (TimeoutResult, error) {
	s.pinPendingFinish(nonce)
	defer s.unpinPendingFinish(nonce)

	s.mu.Lock()
	hostIdx := int(nonce % uint64(len(s.group)))
	hostID := s.HostLabel(hostIdx)
	s.mu.Unlock()

	result := TimeoutResult{Reason: "error"}
	logFields := func(extra ...any) []any {
		base := []any{"escrow", s.escrowID, "nonce", nonce, "host", hostID}
		return append(base, extra...)
	}

	// ConfirmStart is queued from the receipt but often not composed yet.
	// VerifyErrorMiss rejects Pending as no_finish_tx (Started/Finished only),
	// and pairwise AB can leave a second nonce in flight so no heartbeat
	// drains the receipt first. Publish catch-up while the Finish stays pinned.
	st := s.sm.SnapshotState()
	if rec, ok := st.Inferences[nonce]; !ok {
		return result, fmt.Errorf("error miss inference %d not found", nonce)
	} else if rec.Status == types.StatusPending {
		if err := s.SendPendingDiff(ctx); err != nil {
			return result, fmt.Errorf("publish receipt before error miss: %w", err)
		}
	}

	logging.Stage(ctx, "timeout_started", logFields("reason", result.Reason)...)

	verifiers := s.TimeoutVerifiers()
	votes, _, rejectCauses, err := s.CollectErrorMissVotes(ctx, nonce, verifiers, nil, host.TimeoutArtifacts{
		FinishTx:        finishTx,
		ResponsePayload: responsePayload,
	})
	if err != nil {
		return result, fmt.Errorf("collect error-miss votes: %w", err)
	}
	result.Votes = len(votes)
	result.VerifyRejects = rejectCauses
	if len(responsePayload) > 0 {
		sum := sha256.Sum256(responsePayload)
		result.ResponseHash = sum[:]
	}

	if !s.HasSufficientErrorMissVotes(votes) {
		logging.Stage(ctx, "timeout_insufficient_votes", logFields("reason", result.Reason)...)
		return result, fmt.Errorf("inference %d timed out but insufficient votes", nonce)
	}

	_, sendErr := s.sendPendingDiff(ctx, []*types.DevshardTx{errorMissTx(nonce, votes)}, []uint64{nonce})
	rec, ok := s.sm.Inference(nonce)
	if !ok || rec.Status != types.StatusTimedOut {
		logging.Stage(ctx, "timeout_not_applied", logFields("reason", result.Reason)...)
		return result, fmt.Errorf("inference %d error timeout not applied", nonce)
	}
	if sendErr != nil {
		logging.Stage(ctx, "timeout_diff_send_failed", logFields("reason", result.Reason, "error", sendErr)...)
		return result, fmt.Errorf("send error-miss diff: %w", sendErr)
	}
	s.mu.Lock()
	if o, ok := s.nonceStates[nonce]; ok {
		o.finished = false
	}
	s.mu.Unlock()
	result.Accepted = true
	logging.Stage(ctx, "timeout_completed", logFields("reason", result.Reason)...)
	return result, fmt.Errorf("inference %d timed out: error: %w", nonce, ErrInferenceMissed)
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

// Close stops the heartbeat loop (if started) and releases the underlying
// storage, if any. Safe to call multiple times.
func (s *Session) Close() error {
	s.StopHeartbeatLoop()
	s.stopHeightSeedLoop()
	s.stopHeightSyncFlush()
	if s.store != nil {
		return s.store.Close()
	}
	return nil
}

// TimeoutVerifier contacts a host for timeout verification votes.
type TimeoutVerifier interface {
	VerifyTimeout(ctx context.Context, inferenceID uint64, reason types.TimeoutReason, payload *host.InferencePayload, diffs []types.Diff, artifacts host.TimeoutArtifacts) (accept bool, sig []byte, voterSlot uint32, mempool []*types.DevshardTx, rejectCause string, err error)
	VerifyErrorMiss(ctx context.Context, inferenceID uint64, diffs []types.Diff, artifacts host.TimeoutArtifacts) (accept bool, sig []byte, voterSlot uint32, mempool []*types.DevshardTx, rejectCause string, err error)
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

func isVoteRPCTimeout(err error) bool {
	if err == nil || errors.Is(err, errVerifierQueueExpired) || errors.Is(err, errVoteNotSent) {
		return false
	}
	return errors.Is(err, context.DeadlineExceeded)
}

func logVoteRPC(ctx context.Context, logFields func(string, ...any) []any, verifierAddr string, err error, accept bool, elapsed time.Duration) {
	timedOut := isVoteRPCTimeout(err)
	if timedOut {
		logging.Stage(ctx, "timeout_vote_rpc_timeout",
			logFields(verifierAddr, "elapsed_ms", elapsed.Milliseconds(), "error", err)...,
		)
	}
	if elapsed <= VerifyTimeoutSlowLog {
		return
	}
	outcome := "accept"
	switch {
	case timedOut:
		outcome = "timeout"
	case err != nil:
		outcome = "error"
	case !accept:
		outcome = "reject"
	}
	logging.Stage(ctx, "timeout_vote_slow",
		logFields(verifierAddr, "elapsed_ms", elapsed.Milliseconds(), "outcome", outcome)...,
	)
}

// CollectTimeoutVotes contacts non-executor hosts to collect signed votes.
// Returns accept votes for MsgTimeoutInference and recovery txs copied from
// reject responses (typically the executor's MsgConfirmStart).
// Deduplicates verifiers by validator address to avoid duplicate votes
// when the same validator occupies multiple slots.
//
// Each verifier is sent only the diffs it is missing (diffsForHost). diffs is
// an optional overlay of extra diffs not yet in session history (tests); pass
// nil from HandleTimeout.
func (s *Session) CollectTimeoutVotes(
	ctx context.Context,
	inferenceID uint64,
	reason types.TimeoutReason,
	payload *host.InferencePayload,
	verifiers map[int]TimeoutVerifier, // hostIdx -> verifier
	diffs []types.Diff,
	artifacts ...host.TimeoutArtifacts,
) ([]*types.TimeoutVote, []*types.DevshardTx, []string, error) {
	votes, recovery, rejectCauses, _, err := s.collectTimeoutVotes(ctx, inferenceID, reason, payload, verifiers, diffs, artifacts...)
	return votes, recovery, rejectCauses, err
}

func (s *Session) collectTimeoutVotes(
	ctx context.Context,
	inferenceID uint64,
	reason types.TimeoutReason,
	payload *host.InferencePayload,
	verifiers map[int]TimeoutVerifier,
	diffs []types.Diff,
	artifacts ...host.TimeoutArtifacts,
) ([]*types.TimeoutVote, []*types.DevshardTx, []string, string, error) {
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
		mempool      []*types.DevshardTx
		rejectCause  string
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
				inflight, waiters := s.verifierQueue.snapshot(av.verifierAddr)
				logging.Stage(ctx, "timeout_vote_queue_expired",
					logFields(av.verifierAddr,
						"wait_ms", time.Since(queueStart).Milliseconds(),
						"wait_timeout_ms", VerifierQueueWaitTimeout.Milliseconds(),
						"error", err,
						"inflight", len(inflight),
						"waiters", waiters,
						"inflight_snapshot", formatInflightSnapshot(inflight, time.Now()),
					)...,
				)
				results <- voteResult{err: fmt.Errorf("%w: %w", errVerifierQueueExpired, err), verifierIdx: av.idx, verifierAddr: av.verifierAddr}
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
				results <- voteResult{err: fmt.Errorf("%w: %w", errVoteNotSent, err), verifierIdx: av.idx, verifierAddr: av.verifierAddr}
				return
			}

			catchUp := mergeTimeoutCatchUpDiffs(s.catchUpDiffsForVerifier(av.idx), diffs)
			arts := firstTimeoutArtifacts(artifacts)

			rec := inflightVerify{
				Escrow: s.escrowID,
				Nonce:  inferenceID,
				Reason: timeoutReasonLogLabel(reason),
				SentAt: time.Now(),
			}
			if id, ok := logging.RequestID(ctx); ok {
				rec.RequestID = id
			}
			s.verifierQueue.beginInflight(av.verifierAddr, rec)
			defer s.verifierQueue.endInflight(av.verifierAddr, rec)

			logging.Stage(ctx, "timeout_vote_sent",
				logFields(av.verifierAddr, "sent_at_ms", rec.SentAt.UnixMilli())...,
			)
			accept, sig, voterSlot, mempool, rejectCause, err := av.verifier.VerifyTimeout(ctx, inferenceID, reason, payload, catchUp, arts)
			// The slot is held and a vote is idempotent, so an unanswered request is asked once more.
			if transport.IsTransientWriteError(err) && ctx.Err() == nil {
				logging.Debug("retrying a vote the peer never answered", "subsystem", "session",
					"inference_id", inferenceID, "verifier", av.verifierAddr, "error", err)
				accept, sig, voterSlot, mempool, rejectCause, err = av.verifier.VerifyTimeout(ctx, inferenceID, reason, payload, catchUp, arts)
			}
			logVoteRPC(ctx, logFields, av.verifierAddr, err, accept, time.Since(rec.SentAt))
			if err != nil {
				results <- voteResult{err: err, verifierIdx: av.idx, verifierAddr: av.verifierAddr}
				return
			}
			if !accept {
				results <- voteResult{verifierIdx: av.idx, verifierAddr: av.verifierAddr, mempool: mempool, rejectCause: rejectCause} // nil vote, no error
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
	var recovery []*types.DevshardTx
	var rejectCauses []string
	seenRecovery := make(map[string]struct{})
	expected := len(deduped)

	voteThreshold := s.sm.VoteThreshold()
	var accWeight uint32
	var errors, rejects, invalid int
	errorClasses := make(map[string]int)
	for i := 0; i < expected; i++ {
		res := <-results
		if res.err != nil {
			errors++
			errorClasses[classifyVoteError(res.err)]++
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
			s.forgetHostState(res.verifierIdx, res.err)
			continue // skip failed hosts
		}
		if res.vote != nil {
			// Verify before counting. An unverified vote must never contribute
			// weight, reach the quorum decision, or trigger the early exit
			// below: applyTimeout rejects the whole MsgTimeoutInference on the
			// first bad vote, so a single spoofed slot would otherwise discard
			// an honest quorum that was still in flight.
			if vErr := s.verifyTimeoutVote(inferenceID, reason, res.vote, res.verifierAddr); vErr != nil {
				invalid++
				logging.Stage(ctx, "timeout_vote_result",
					logFields(
						res.verifierAddr,
						"outcome", "invalid",
						"voter_slot", res.vote.VoterSlot,
						"running_weight", accWeight,
						"threshold", voteThreshold,
						"error", vErr,
					)...,
				)
				logging.Warn("rejected unverified timeout vote",
					"subsystem", "session", "escrow_id", s.escrowID,
					"inference_id", inferenceID, "responder", res.verifierAddr,
					"voter_slot", res.vote.VoterSlot, "error", vErr)
				continue
			}
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
			if res.rejectCause != "" {
				rejectCauses = append(rejectCauses, res.rejectCause)
			}
			for _, tx := range host.RecoveryTxsFor(res.mempool, inferenceID) {
				key := hostRecoveryDedupKey(tx)
				if key == "" {
					continue
				}
				if _, dup := seenRecovery[key]; dup {
					continue
				}
				seenRecovery[key] = struct{}{}
				recovery = append(recovery, tx)
			}
			logging.Stage(ctx, "timeout_vote_result",
				logFields(
					res.verifierAddr,
					"outcome", "reject",
					"reject_cause", res.rejectCause,
					"running_weight", accWeight,
					"threshold", voteThreshold,
				)...,
			)
		}
		if accWeight > voteThreshold {
			break
		}
	}
	tallyFields := []any{
		"accept", len(votes),
		"weight", accWeight,
		"reject", rejects,
		"invalid", invalid,
		"errors", errors,
		"threshold", voteThreshold,
		"verifiers", expected,
		"sufficient", accWeight > voteThreshold,
	}
	if classes := formatErrorClasses(errorClasses); classes != "" {
		tallyFields = append(tallyFields, "error_classes", classes)
	}
	logging.Stage(ctx, "timeout_vote_tally",
		logFields("", tallyFields...)...,
	)
	logging.Debug("timeout vote collection",
		"subsystem", "session", "inference_id", inferenceID,
		"accept", len(votes), "weight", accWeight,
		"reject", rejects, "invalid", invalid, "errors", errors,
		"threshold", voteThreshold, "verifiers", expected)

	return votes, recovery, rejectCauses, dominantVoteError(errorClasses), nil
}

func (s *Session) CollectErrorMissVotes(
	ctx context.Context,
	inferenceID uint64,
	verifiers map[int]TimeoutVerifier,
	diffs []types.Diff,
	artifacts host.TimeoutArtifacts,
) ([]*types.ErrorMissVote, []*types.DevshardTx, []string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	executorIdx := int(inferenceID % uint64(len(s.group)))
	executorAddr := s.group[executorIdx].ValidatorAddress

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
		vote         *types.ErrorMissVote
		err          error
		verifierIdx  int
		verifierAddr string
		mempool      []*types.DevshardTx
		rejectCause  string
	}

	logFields := func(hostAddr string, extra ...any) []any {
		base := []any{"escrow", s.escrowID, "nonce", inferenceID, "reason", "error"}
		if hostAddr != "" {
			base = append(base, "host", shortAddress(hostAddr))
		}
		return append(base, extra...)
	}

	results := make(chan voteResult, len(deduped))
	for _, av := range deduped {
		logging.Stage(ctx, "timeout_vote_requested", logFields(av.verifierAddr)...)
		go func(av addrVerifier) {
			queueStart := time.Now()
			waitCtx, waitCancel := context.WithTimeout(ctx, VerifierQueueWaitTimeout)
			err := s.verifierQueue.acquire(waitCtx, av.verifierAddr)
			waitCancel()
			if err != nil {
				results <- voteResult{err: err, verifierIdx: av.idx, verifierAddr: av.verifierAddr}
				return
			}
			waitMs := time.Since(queueStart).Milliseconds()
			if waitMs > 0 {
				logging.Stage(ctx, "timeout_vote_dequeued", logFields(av.verifierAddr, "wait_ms", waitMs)...)
			}
			defer s.verifierQueue.release(av.verifierAddr)
			if err := ctx.Err(); err != nil {
				results <- voteResult{err: err, verifierIdx: av.idx, verifierAddr: av.verifierAddr}
				return
			}
			accept, sig, voterSlot, mempool, rejectCause, err := av.verifier.VerifyErrorMiss(ctx, inferenceID, mergeTimeoutCatchUpDiffs(s.catchUpDiffsForVerifier(av.idx), diffs), artifacts)
			if err != nil {
				results <- voteResult{err: err, verifierIdx: av.idx, verifierAddr: av.verifierAddr}
				return
			}
			if !accept {
				results <- voteResult{verifierIdx: av.idx, verifierAddr: av.verifierAddr, mempool: mempool, rejectCause: rejectCause}
				return
			}
			results <- voteResult{vote: &types.ErrorMissVote{
				VoterSlot: voterSlot,
				Accept:    true,
				Signature: sig,
			}, verifierIdx: av.idx, verifierAddr: av.verifierAddr}
		}(av)
	}

	var votes []*types.ErrorMissVote
	var recovery []*types.DevshardTx
	var rejectCauses []string
	seenRecovery := make(map[string]struct{})
	expected := len(deduped)
	voteThreshold := s.sm.VoteThreshold()
	var accWeight uint32
	var errors, rejects int
	for i := 0; i < expected; i++ {
		res := <-results
		if res.err != nil {
			errors++
			logging.Stage(ctx, "timeout_vote_result", logFields(res.verifierAddr, "accept", false, "error", res.err)...)
			continue
		}
		if res.vote == nil {
			rejects++
			if res.rejectCause != "" {
				rejectCauses = append(rejectCauses, res.rejectCause)
			}
			logging.Stage(ctx, "timeout_vote_result", logFields(res.verifierAddr, "accept", false, "reject_cause", res.rejectCause)...)
			for _, tx := range host.RecoveryTxsFor(res.mempool, inferenceID) {
				key := hostRecoveryDedupKey(tx)
				if key == "" {
					continue
				}
				if _, ok := seenRecovery[key]; ok {
					continue
				}
				seenRecovery[key] = struct{}{}
				recovery = append(recovery, tx)
			}
			continue
		}
		votes = append(votes, res.vote)
		addr := s.sm.SlotAddress(res.vote.VoterSlot)
		accWeight += s.sm.AddressSlotCount(addr)
		logging.Stage(ctx, "timeout_vote_result", logFields(res.verifierAddr, "accept", true)...)
		if accWeight > voteThreshold {
			cancel()
		}
	}

	logging.Stage(ctx, "timeout_votes_collected",
		logFields("", "accept", len(votes), "weight", accWeight, "reject", rejects, "errors", errors, "threshold", voteThreshold, "verifiers", expected, "sufficient", accWeight > voteThreshold)...,
	)
	return votes, recovery, rejectCauses, nil
}

func firstTimeoutArtifacts(artifacts []host.TimeoutArtifacts) host.TimeoutArtifacts {
	if len(artifacts) == 0 {
		return host.TimeoutArtifacts{}
	}
	return artifacts[0]
}

// catchUpDiffsForVerifier returns diffs this host has not yet been sent.
// The slice is copied under s.mu so the RPC can proceed without holding the lock.
func (s *Session) catchUpDiffsForVerifier(hostIdx int) []types.Diff {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.diffsForHost(hostIdx)
	if len(src) == 0 {
		return nil
	}
	out := make([]types.Diff, len(src))
	copy(out, src)
	return out
}

// mergeTimeoutCatchUpDiffs prefers the per-verifier catch-up set and appends
// any extra diffs (tests injecting unpublished diffs) whose nonce is not already
// present. Production HandleTimeout passes extra=nil so a long-lived session
// does not put the full history on the verify-timeout body beside the error payload.
func mergeTimeoutCatchUpDiffs(catchUp, extra []types.Diff) []types.Diff {
	if len(extra) == 0 {
		return catchUp
	}
	if len(catchUp) == 0 {
		return extra
	}
	seen := make(map[uint64]struct{}, len(catchUp))
	for _, d := range catchUp {
		seen[d.Nonce] = struct{}{}
	}
	out := catchUp
	for _, d := range extra {
		if _, ok := seen[d.Nonce]; ok {
			continue
		}
		out = append(out, d)
		seen[d.Nonce] = struct{}{}
	}
	return out
}

// HasSufficientTimeoutVotes returns true if the accept votes exceed the vote
// threshold. Votes must already have been verified against their responder --
// CollectTimeoutVotes only returns verified votes -- since this counts the
// weight of whatever slot each vote claims.
//
// Weight is deduplicated by validator address, matching applyTimeout: one
// address cannot contribute its slot count more than once regardless of how
// many of its slots appear in the vote list.
func (s *Session) HasSufficientTimeoutVotes(votes []*types.TimeoutVote) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	threshold := s.sm.VoteThreshold()
	counted := make(map[string]bool, len(votes))
	var accWeight uint32
	for _, v := range votes {
		if !v.Accept {
			continue
		}
		addr := s.sm.SlotAddress(v.VoterSlot)
		if counted[addr] {
			continue
		}
		counted[addr] = true
		accWeight += s.sm.AddressSlotCount(addr)
	}
	return accWeight > threshold
}

func (s *Session) HasSufficientErrorMissVotes(votes []*types.ErrorMissVote) bool {
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
