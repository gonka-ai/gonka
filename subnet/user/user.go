package user

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"sync"

	"google.golang.org/protobuf/proto"

	"subnet"
	"subnet/host"
	"subnet/logging"
	"subnet/signing"
	"subnet/state"
	"subnet/storage"
	"subnet/types"
)

type HostClient interface {
	Send(ctx context.Context, req host.HostRequest) (*host.HostResponse, error)
}

type InProcessClient struct {
	Host *host.Host
}

func (c *InProcessClient) Send(ctx context.Context, req host.HostRequest) (*host.HostResponse, error) {
	resp, err := c.Host.HandleRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.ExecutionJob != nil {
		_, execErr := c.Host.RunExecution(ctx, resp.ExecutionJob)
		if execErr != nil {
			logging.Error("deferred execution failed", "subsystem", "in_process_client", "error", execErr)
		}
		// Re-fetch mempool after execution.
		resp.Mempool = c.Host.MempoolTxs()
	}
	return resp, nil
}

// InferenceParams describes a new inference to send.
type InferenceParams struct {
	Model       string
	Prompt      []byte
	InputLength uint64
	MaxTokens   uint64
	StartedAt   int64
}

// Session manages the user side of the subnet protocol.
type Session struct {
	mu            sync.Mutex
	sm            *state.StateMachine
	signer        signing.Signer
	verifier      signing.Verifier
	escrowID      string
	group         []types.SlotAssignment
	addrToSlots   map[string][]uint32 // validator address -> slot IDs
	clients       []HostClient
	nonce         uint64
	diffs         []types.Diff                 // append-only log
	hostSyncNonce map[int]uint64               // hostIdx -> last nonce sent
	pendingTxs    []*types.SubnetTx            // from host mempools, for next diff
	pendingTxKeys map[string]struct{}          // dedup set keyed by tx_type:id
	signatures    map[uint64]map[uint32][]byte // nonce -> slotID -> sig
	store         storage.Storage              // optional persistent storage
}

// SessionOption configures optional Session behavior.
type SessionOption func(*Session)

// WithStorage sets a persistent storage backend for the session.
// When set, diffs and signatures are persisted on each state transition.
func WithStorage(s storage.Storage) SessionOption {
	return func(sess *Session) { sess.store = s }
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
		sm:            sm,
		signer:        signer,
		verifier:      verifier,
		escrowID:      escrowID,
		group:         group,
		addrToSlots:   addrToSlots,
		clients:       clients,
		hostSyncNonce: make(map[int]uint64),
		pendingTxKeys: make(map[string]struct{}),
		signatures:    make(map[uint64]map[uint32][]byte),
	}
	for _, opt := range opts {
		opt(sess)
	}
	return sess, nil
}

// txPriority returns a sort key for pending tx ordering.
// The state machine requires ConfirmStart before FinishInference before Validation.
func txPriority(tx *types.SubnetTx) int {
	switch tx.GetTx().(type) {
	case *types.SubnetTx_ConfirmStart:
		return 0
	case *types.SubnetTx_FinishInference:
		return 1
	case *types.SubnetTx_Validation:
		return 2
	case *types.SubnetTx_ValidationVote:
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

// processResponse updates session state from a host response.
// inferenceNonce is the nonce assigned during PrepareInference (the logical inference ID).
// resp.Nonce may differ when the host has already advanced past inferenceNonce.
// Caller must hold s.mu.
func (s *Session) processResponse(hostIdx int, resp *host.HostResponse, inferenceNonce uint64) error {
	// Verify state hash if the host returned one.
	if len(resp.StateHash) > 0 {
		idx := int(resp.Nonce) - 1
		var expected []byte
		if idx >= 0 && idx < len(s.diffs) {
			expected = s.diffs[idx].PostStateRoot
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
		s.addPendingTx(&types.SubnetTx{
			Tx: &types.SubnetTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
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
}

// composeDiffLocked builds, applies, persists, and returns a new diff.
// extraTxs are prepended to pending txs. Caller must hold s.mu.
func (s *Session) composeDiffLocked(extraTxs []*types.SubnetTx) (types.Diff, int, error) {
	nonce := s.nonce + 1
	hostIdx := int(nonce % uint64(len(s.group)))

	sort.SliceStable(s.pendingTxs, func(i, j int) bool {
		return txPriority(s.pendingTxs[i]) < txPriority(s.pendingTxs[j])
	})

	candidates := make([]*types.SubnetTx, 0, len(s.pendingTxs)+len(extraTxs))
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
	}

	return diff, hostIdx, nil
}

// PrepareInference composes a diff, applies it locally, advances nonce,
// and returns everything needed for the HTTP send. Thread-safe.
func (s *Session) PrepareInference(params InferenceParams) (*PreparedInference, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	nonce := s.nonce + 1
	promptHash, err := subnet.CanonicalPromptHash(params.Prompt)
	if err != nil {
		return nil, fmt.Errorf("canonical prompt hash: %w", err)
	}
	startTx := &types.SubnetTx{Tx: &types.SubnetTx_StartInference{
		StartInference: &types.MsgStartInference{
			InferenceId: nonce,
			Model:       params.Model,
			PromptHash:  promptHash,
			InputLength: params.InputLength,
			MaxTokens:   params.MaxTokens,
			StartedAt:   params.StartedAt,
		},
	}}

	diff, hostIdx, err := s.composeDiffLocked([]*types.SubnetTx{startTx})
	if err != nil {
		return nil, err
	}

	catchUp := s.diffsForHost(hostIdx)
	return &PreparedInference{
		diff:    diff,
		hostIdx: hostIdx,
		catchUp: catchUp,
		params:  params,
	}, nil
}

// Nonce returns the nonce assigned to this prepared inference.
func (p *PreparedInference) Nonce() uint64 { return p.diff.Nonce }

// HostIdx returns the host index this inference targets.
func (p *PreparedInference) HostIdx() int { return p.hostIdx }

// SendOnly sends a prepared inference to the host and returns the raw response
// without processing it. Use ProcessResponse separately to apply the response
// to session state. This split allows parallel network I/O with ordered processing.
func (s *Session) SendOnly(ctx context.Context, p *PreparedInference) (*host.HostResponse, error) {
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
	})
}

// SendInference composes diff, sends to correct host, processes response.
func (s *Session) SendInference(ctx context.Context, params InferenceParams) (*host.HostResponse, error) {
	p, err := s.PrepareInference(params)
	if err != nil {
		return nil, err
	}
	resp, err := s.SendOnly(ctx, p)
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
func (s *Session) sendDiffRound(ctx context.Context, extraTxs []*types.SubnetTx) error {
	s.mu.Lock()
	diff, hostIdx, err := s.composeDiffLocked(extraTxs)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	catchUp := s.diffsForHost(hostIdx)
	s.mu.Unlock()

	resp, err := s.clients[hostIdx].Send(ctx, host.HostRequest{Diffs: catchUp, Nonce: diff.Nonce})
	if err != nil {
		return nil // dead host, not fatal
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.processResponse(hostIdx, resp, diff.Nonce)
}

// sendCatchUp sends existing diffs to a host without composing new ones.
// Returns non-nil only on processResponse errors; dead hosts are silently skipped.
func (s *Session) sendCatchUp(ctx context.Context, hostIdx int) error {
	s.mu.Lock()
	nonce := s.nonce
	catchUp := s.diffsForHost(hostIdx)
	s.mu.Unlock()

	resp, err := s.clients[hostIdx].Send(ctx, host.HostRequest{Diffs: catchUp, Nonce: nonce})
	if err != nil {
		return nil // dead host
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.processResponse(hostIdx, resp, nonce)
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
	n := len(s.group)

	finalizeTx := &types.SubnetTx{Tx: &types.SubnetTx_FinalizeRound{
		FinalizeRound: &types.MsgFinalizeRound{},
	}}

	// Phase A: N diffs collecting remaining txs. First carries MsgFinalizeRound.
	for i := 0; i < n; i++ {
		var extra []*types.SubnetTx
		if i == 0 {
			extra = []*types.SubnetTx{finalizeTx}
		}
		if err := s.sendDiffRound(ctx, extra); err != nil {
			return err
		}
	}

	// Phase A+1: drain the last host's reveal.
	if err := s.sendDiffRound(ctx, nil); err != nil {
		return err
	}

	// Phase B: propagate complete state, collect signatures.
	for hostIdx := 0; hostIdx < n; hostIdx++ {
		if err := s.sendCatchUp(ctx, hostIdx); err != nil {
			return err
		}
	}

	// Check signature quorum: need 2/3+1 slot-weighted signatures.
	var sigWeight uint32
	finalNonce := s.nonce
	if sigs, ok := s.signatures[finalNonce]; ok {
		counted := make(map[string]bool)
		for slotID := range sigs {
			addr := s.sm.SlotAddress(slotID)
			if counted[addr] {
				continue
			}
			counted[addr] = true
			sigWeight += s.sm.AddressSlotCount(addr)
		}
	}
	threshold := 2*s.sm.TotalSlots()/3 + 1
	if sigWeight < threshold {
		return fmt.Errorf("insufficient signatures: %d/%d weight", sigWeight, threshold)
	}

	return nil
}

// signDiff builds and signs a diff with the given nonce, txs, and post_state_root.
func (s *Session) signDiff(nonce uint64, txs []*types.SubnetTx, postStateRoot []byte) (types.Diff, error) {
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

// subnetTxKey returns a dedup key for host-proposed txs.
// Returns "" for user-proposed types (start, finalize, timeout).
func subnetTxKey(tx *types.SubnetTx) string {
	switch inner := tx.GetTx().(type) {
	case *types.SubnetTx_FinishInference:
		return fmt.Sprintf("finish:%d", inner.FinishInference.InferenceId)
	case *types.SubnetTx_ConfirmStart:
		return fmt.Sprintf("confirm:%d", inner.ConfirmStart.InferenceId)
	case *types.SubnetTx_Validation:
		return fmt.Sprintf("validation:%d:%d", inner.Validation.InferenceId, inner.Validation.ValidatorSlot)
	case *types.SubnetTx_ValidationVote:
		return fmt.Sprintf("vote:%d:%d", inner.ValidationVote.InferenceId, inner.ValidationVote.VoterSlot)
	case *types.SubnetTx_RevealSeed:
		return fmt.Sprintf("reveal_seed:%d", inner.RevealSeed.SlotId)
	default:
		return ""
	}
}

// addPendingTx appends tx to pendingTxs if not a duplicate.
func (s *Session) addPendingTx(tx *types.SubnetTx) {
	key := subnetTxKey(tx)
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

func (s *Session) PendingTxs() []*types.SubnetTx {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingTxs
}

func (s *Session) StateMachine() *state.StateMachine { return s.sm }

// AddPendingTimeoutTx adds a MsgTimeoutInference to the pending tx queue.
func (s *Session) AddPendingTimeoutTx(inferenceID uint64, reason types.TimeoutReason, votes []*types.TimeoutVote) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addPendingTx(&types.SubnetTx{
		Tx: &types.SubnetTx_TimeoutInference{TimeoutInference: &types.MsgTimeoutInference{
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

	resp, err := s.clients[hostIdx].Send(ctx, host.HostRequest{Diffs: catchUp, Nonce: diff.Nonce})
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
	// Determine executor slot and resolve its validator address.
	executorIdx := int(inferenceID % uint64(len(s.group)))
	executorAddr := s.group[executorIdx].ValidatorAddress

	// Dedup verifiers by address to avoid duplicate votes from multi-slot validators.
	// Pre-seed the executor's address so ALL slots owned by that validator are excluded,
	// not just the single executor index. This prevents a multi-slot executor from
	// voting on its own timeout through a different slot.
	type addrVerifier struct {
		idx      int
		verifier TimeoutVerifier
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
		deduped = append(deduped, addrVerifier{idx, v})
	}

	type voteResult struct {
		vote *types.TimeoutVote
		err  error
	}

	results := make(chan voteResult, len(deduped))
	for _, av := range deduped {
		go func(verifier TimeoutVerifier) {
			accept, sig, voterSlot, err := verifier.VerifyTimeout(ctx, inferenceID, reason, payload, diffs)
			if err != nil {
				results <- voteResult{err: err}
				return
			}
			if !accept {
				results <- voteResult{} // nil vote, no error
				return
			}
			results <- voteResult{vote: &types.TimeoutVote{
				VoterSlot: voterSlot,
				Accept:    true,
				Signature: sig,
			}}
		}(av.verifier)
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
			logging.Debug("timeout vote error",
				"subsystem", "session", "inference_id", inferenceID, "error", res.err)
			continue // skip failed hosts
		}
		if res.vote != nil {
			votes = append(votes, res.vote)
			voterAddr := s.sm.SlotAddress(res.vote.VoterSlot)
			accWeight += s.sm.AddressSlotCount(voterAddr)
		} else {
			rejects++
		}
		if accWeight > voteThreshold {
			break
		}
	}
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
