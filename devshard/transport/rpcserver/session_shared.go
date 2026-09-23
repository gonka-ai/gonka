package rpcserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"time"

	"connectrpc.com/connect"

	"devshard/transport/rpcpb"
)

var (
	errAttachNonceUsed = errors.New("attach_nonce already in use")
	errAttachStale     = errors.New("attach is not newer than the live session")
	errTooManySessions = errors.New("too many sessions")
)

func tokenHashHex(token []byte) string {
	sum := sha256.Sum256(token)
	return hex.EncodeToString(sum[:])
}

func tokenHashBytes(token []byte) []byte {
	sum := sha256.Sum256(token)
	out := make([]byte, len(sum))
	copy(out, sum[:])
	return out
}

func admitUntil(state string, expires time.Time, grace *time.Time) time.Time {
	if state == sessionStateLive || grace == nil {
		return expires
	}
	return *grace
}

// sessionStoreQueries is zero when this process is the memory store.
// Postgres increments it only for SQL. Lookup of an unknown token does not.
func (h *PeerAuthHandler) sessionStoreQueries() int64 {
	if h == nil || h.shared == nil {
		return 0
	}
	return h.shared.SQLQueries()
}

// SessionsReady is true for the memory store. A Postgres child is ready
// only after the initial session load has been applied.
func (h *PeerAuthHandler) SessionsReady() bool {
	if h == nil || h.shared == nil {
		return true
	}
	return h.shared.Ready()
}

// EnableShared replicates sessions through s. The handler maps stay the
// admission cache: SQL fills them, and an unknown token never reads SQL.
func (h *PeerAuthHandler) EnableShared(s *SharedSessions) {
	if h == nil || s == nil {
		return
	}
	h.mu.Lock()
	h.shared = s
	if h.byHash == nil {
		h.byHash = make(map[string]*peerSession)
	}
	h.mu.Unlock()
	s.apply = h.applySharedBatch
	s.Start()
}

// SetPublishedBarrier records the router membership for this version.
// No publish leaves the member-table barrier in place.
func (h *PeerAuthHandler) SetPublishedBarrier(self string, ids []string) {
	if h == nil || h.shared == nil {
		return
	}
	h.shared.SetPublishedBarrier(self, ids)
}

func (h *PeerAuthHandler) sharedPeerLive(addr string) bool {
	if h == nil || h.byHash == nil || addr == "" {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	now := h.now()
	for _, sess := range h.byHash {
		if sess != nil && sess.current && sess.peer == addr && !now.After(sess.expires) {
			return true
		}
	}
	return false
}

func (h *PeerAuthHandler) attachShared(ctx context.Context, peer string, token []byte, attached int64, wasLive bool, chargedAt time.Time) (*connect.Response[rpcpb.AttachResponse], error) {
	out, err := h.shared.Commit(ctx, attachCommit{
		Peer:         peer,
		Hash:         tokenHashBytes(token),
		AttachedUnix: attached,
		TTL:          h.cfg.SessionTTL,
		Grace:        h.cfg.TokenGrace,
		MaxSessions:  h.cfg.MaxSessions,
	})
	if err != nil {
		return nil, mapSharedAttachErr(err)
	}
	h.applySharedBatch(out.Rows, token)
	h.shared.WaitApplied(ctx, out.Seq)
	if wasLive {
		h.refundAttach(chargedAt)
	}
	expires := out.Expires
	if expires.IsZero() {
		expires = h.now().Add(h.cfg.SessionTTL)
	}
	return connect.NewResponse(&rpcpb.AttachResponse{
		SessionToken: token,
		ExpiresAt:    expires.Unix(),
		Limits:       h.advertisedRateLimits(),
	}), nil
}

func mapSharedAttachErr(err error) error {
	switch {
	case errors.Is(err, errAttachNonceUsed), errors.Is(err, errAttachStale):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, errTooManySessions):
		return connect.NewError(connect.CodeResourceExhausted, err)
	default:
		return connect.NewError(connect.CodeUnavailable, err)
	}
}

func (h *PeerAuthHandler) applySharedBatch(rows []sessionRow, raw []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed.Load() {
		return
	}
	if h.byHash == nil {
		h.byHash = make(map[string]*peerSession)
	}
	sorted := append([]sessionRow(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })
	for _, row := range sorted {
		h.applyOneLocked(row)
	}
	if len(raw) > 0 {
		h.bindRawTokenLocked(raw)
	}
	h.observeSizesLocked()
}

func (h *PeerAuthHandler) applyOneLocked(row sessionRow) {
	if h.shared != nil && (row.Host != h.shared.host || row.Version != h.shared.version) {
		return
	}
	hexHash := row.HashHex
	if hexHash == "" && len(row.Hash) > 0 {
		hexHash = hex.EncodeToString(row.Hash)
	}
	if hexHash == "" {
		return
	}
	sess := h.byHash[hexHash]
	if sess != nil && row.Seq > 0 && sess.seq > 0 && row.Seq < sess.seq {
		return
	}
	if h.now().After(row.AdmitUntil) {
		if sess != nil {
			h.forgetSharedLocked(hexHash, sess)
		}
		return
	}
	if sess == nil {
		sess = &peerSession{tokenHash: hexHash}
		h.byHash[hexHash] = sess
	}
	sess.peer = row.Peer
	sess.expires = row.AdmitUntil
	sess.attached = row.AttachedUnix
	sess.tokenHash = hexHash
	if row.Seq > 0 {
		sess.seq = row.Seq
	}
	if sess.created.IsZero() {
		sess.created = h.now()
	}
	sess.current = row.State == sessionStateLive
	if !sess.current {
		sess.stopWatchLocked()
		if sess.rawKey != "" && h.byPeer[row.Peer] == sess.rawKey {
			h.prevByPeer[row.Peer] = sess.rawKey
			delete(h.byPeer, row.Peer)
		}
		return
	}
	if sess.rawKey != "" {
		h.sessions[sess.rawKey] = sess
		h.byPeer[row.Peer] = sess.rawKey
	}
}

func (h *PeerAuthHandler) sweepSharedCache(now time.Time) {
	if h == nil || h.byHash == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for hash, sess := range h.byHash {
		if sess == nil || !now.After(sess.expires) {
			continue
		}
		h.forgetSharedLocked(hash, sess)
	}
}

func (h *PeerAuthHandler) forgetSharedLocked(hexHash string, sess *peerSession) {
	sess.stopWatchLocked()
	if sess.rawKey != "" {
		h.dropSessionLocked(sess.rawKey, sess)
		return
	}
	delete(h.byHash, hexHash)
}

func (h *PeerAuthHandler) bindRawTokenLocked(raw []byte) {
	if len(raw) == 0 || h.byHash == nil {
		return
	}
	hexHash := tokenHashHex(raw)
	sess := h.byHash[hexHash]
	if sess == nil {
		return
	}
	tok := rawTokenKey(raw)
	sess.rawKey = tok
	h.sessions[tok] = sess
	if sess.current {
		h.byPeer[sess.peer] = tok
	}
}
