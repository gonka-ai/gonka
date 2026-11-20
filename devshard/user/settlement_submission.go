package user

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"devshard/host"
	"devshard/transport"
)

// SettlementGrantQuery reads current warm grantees of a participant directly
// from the chain. Each submission check uses a separate, uncached view.
type SettlementGrantQuery func(context.Context, string) (map[string]struct{}, error)

var ErrSettlementSignaturesChanged = errors.New("settlement signatures need reassembly")

// SettlementQuorum assembles a submission for one frozen v7 escrow state.
// Replacement candidates are kept apart from the historical signature pool.
type SettlementQuorum struct {
	session    *Session
	nonce      uint64
	root       []byte
	candidates map[uint32]map[string][]byte
}

// NewSettlementQuorum captures the final state identity and its known signatures.
func (s *Session) NewSettlementQuorum() (*SettlementQuorum, error) {
	nonce := s.Nonce()
	s.mu.Lock()
	final := s.isCurrentFinalStateLocked(nonce)
	s.mu.Unlock()
	if !final {
		return nil, fmt.Errorf("escrow %s has no v7 settlement state", s.escrowID)
	}
	raw, root, err := s.signatureSnapshot(nonce)
	if err != nil {
		return nil, err
	}
	q := &SettlementQuorum{session: s, nonce: nonce, root: root, candidates: make(map[uint32]map[string][]byte)}
	q.add(raw, "")
	return q, nil
}

func (q *SettlementQuorum) add(sigs map[uint32][]byte, owner string) {
	for slot, sig := range sigs {
		cold := q.session.sm.SlotAddress(slot)
		if cold == "" || owner != "" && owner != cold {
			continue
		}
		signer, err := q.session.recoverStateSignatureAddress(q.nonce, q.root, sig)
		if err != nil {
			continue
		}
		if signer == cold {
			for _, owned := range q.session.addrToSlots[cold] {
				q.addCandidate(owned, signer, sig)
			}
		} else {
			// Only the issuer's labeled slot may use this warm signature.
			q.addCandidate(slot, signer, sig)
		}
	}
}

func (q *SettlementQuorum) addCandidate(slot uint32, signer string, sig []byte) {
	if q.candidates[slot] == nil {
		q.candidates[slot] = make(map[string][]byte)
	}
	if _, exists := q.candidates[slot][signer]; !exists {
		q.candidates[slot][signer] = bytes.Clone(sig)
	}
}

type settlementGrantView struct {
	warm map[string]struct{}
	err  error
}

// selectSignatures combines historical eligibility at the signed checkpoint
// with current submission authority. One eligible signature counts per slot.
func (q *SettlementQuorum) selectSignatures(ctx context.Context, query SettlementGrantQuery, only map[uint32][]byte) (map[uint32][]byte, map[uint32]string) {
	const authorityBudget = 10 * time.Second
	ctx, cancel := context.WithTimeout(ctx, authorityBudget)
	defer cancel()
	actors := q.session.sm.HostSlotActorsAtWithContext(ctx, q.nonce, authorityBudget)
	current := make(map[string]settlementGrantView)
	selected := make(map[uint32][]byte)
	missing := make(map[uint32]string)
	for _, slot := range q.session.group {
		if only != nil && only[slot.SlotID] == nil {
			continue
		}
		cold := slot.ValidatorAddress
		candidates := q.candidates[slot.SlotID]
		if sig := candidates[cold]; sig != nil && (only == nil || bytes.Equal(only[slot.SlotID], sig)) {
			selected[slot.SlotID] = sig
			continue
		}
		reason := "no eligible signature"
		var warmSigners []string
		for signer, sig := range candidates {
			if only != nil && !bytes.Equal(only[slot.SlotID], sig) {
				continue
			}
			if actors.Allows(slot.SlotID, signer) {
				warmSigners = append(warmSigners, signer)
			} else if actors.AuthorizationError != nil && actors.AuthorizationError(slot.SlotID, signer) != nil {
				reason = "historical grant unavailable"
			}
		}
		if len(warmSigners) > 0 {
			view, checked := current[cold]
			if !checked {
				if query == nil {
					view.err = errors.New("no current grant source")
				} else {
					view.warm, view.err = query(ctx, cold)
				}
				current[cold] = view
			}
			if view.err != nil {
				reason = "current grant unavailable"
			} else {
				reason = "warm grant ended"
				slices.Sort(warmSigners)
				for _, signer := range warmSigners {
					if _, ok := view.warm[signer]; ok {
						selected[slot.SlotID] = candidates[signer]
						break
					}
				}
			}
		}
		if selected[slot.SlotID] == nil {
			missing[slot.SlotID] = reason
		}
	}
	return selected, missing
}

// Build collects replacements when the currently submit-eligible weight is
// insufficient. It sends no new diff and does not change the final state.
func (q *SettlementQuorum) Build(ctx context.Context, query SettlementGrantQuery) (map[uint32][]byte, error) {
	selected, missing := q.selectSignatures(ctx, query, nil)
	threshold := q.session.sm.QuorumThreshold()
	if uint32(len(selected)) >= threshold {
		return selected, nil
	}
	q.collect(ctx, missing)
	selected, missing = q.selectSignatures(ctx, query, nil)
	if uint32(len(selected)) >= threshold {
		return selected, nil
	}
	return nil, q.weightError(len(selected), threshold, missing)
}

func (q *SettlementQuorum) weightError(weight int, threshold uint32, missing map[uint32]string) error {
	var participants []string
	for _, slot := range q.session.group {
		if reason := missing[slot.SlotID]; reason != "" {
			participants = append(participants, fmt.Sprintf("slot %d (%s): %s", slot.SlotID, slot.ValidatorAddress, reason))
		}
	}
	return fmt.Errorf("escrow %s nonce %d: settlement has %d/%d submit-eligible weight; %v",
		q.session.escrowID, q.nonce, weight, threshold, participants)
}

// Validate checks the exact signatures prepared for broadcast. A lost grant
// invalidates that transaction even if enough other signatures remain.
func (q *SettlementQuorum) Validate(ctx context.Context, query SettlementGrantQuery, nonce uint64, root []byte, signatures map[uint32][]byte) error {
	if nonce != q.nonce || !bytes.Equal(root, q.root) {
		return fmt.Errorf("settlement no longer matches frozen nonce %d and state root", q.nonce)
	}
	selected, missing := q.selectSignatures(ctx, query, signatures)
	threshold := q.session.sm.QuorumThreshold()
	if len(selected) != len(signatures) || uint32(len(selected)) < threshold {
		return fmt.Errorf("%w: %w", ErrSettlementSignaturesChanged, q.weightError(len(selected), threshold, missing))
	}
	return nil
}

func (q *SettlementQuorum) collect(ctx context.Context, missing map[uint32]string) {
	s := q.session
	clients := s.getFinalizeClients()
	queried := make(map[string]bool)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for idx, slot := range s.group {
		cold := slot.ValidatorAddress
		if queried[cold] || clients[idx] == nil {
			continue
		}
		if !slices.ContainsFunc(s.addrToSlots[cold], func(id uint32) bool { return missing[id] != "" }) {
			continue
		}
		queried[cold] = true
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, s.signatureCollectHostTimeout)
			defer cancel()
			q.collectFrom(ctx, idx, cold, clients[idx], &mu)
		})
	}
	wg.Wait()
}

func (q *SettlementQuorum) collectFrom(ctx context.Context, idx int, cold string, client HostClient, mu *sync.Mutex) {
	s := q.session
	fetch := func() {
		if fetcher, ok := client.(SignatureFetcher); ok {
			if signatures, err := fetcher.GetSignatures(ctx, q.nonce); err == nil {
				mu.Lock()
				q.add(signatures, cold)
				mu.Unlock()
			}
		}
	}
	fetch()
	if err := s.sendCatchUpThrough(ctx, idx, client, q.nonce); err != nil {
		s.forgetHostState(idx, err)
	}
	request := host.HostRequest{Nonce: q.nonce, HeightSyncEscrow: s.heightSyncEscrowHints()}
	resp, err := client.Send(ctx, request, nil, nil)
	if transport.IsSessionNotFound(err) && s.RewindHostCatchUp(idx, "settlement host lost its escrow") {
		if s.sendCatchUpThrough(ctx, idx, client, q.nonce) == nil {
			resp, err = client.Send(ctx, request, nil, nil)
		}
	}
	if err == nil && resp != nil && resp.Nonce == q.nonce && bytes.Equal(resp.StateHash, q.root) {
		// A cold key authenticates its own slots. Warm responses must be
		// obtained from the issuer's slot-indexed signature endpoint.
		if signer, err := s.recoverStateSignatureAddress(q.nonce, q.root, resp.StateSig); err == nil && signer == cold {
			mu.Lock()
			for _, slot := range s.addrToSlots[cold] {
				q.addCandidate(slot, cold, resp.StateSig)
			}
			mu.Unlock()
		}
	}
	fetch()
}
