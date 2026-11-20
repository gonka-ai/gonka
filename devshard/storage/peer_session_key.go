package storage

import (
	"context"
	"crypto/rand"
	"fmt"

	"devshard/signing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PeerSessionKeyProvider supplies a shared key independently of a replica's
// warm signer. available=false means there is no shared Postgres backend;
// errors from a configured backend must never fall back to a different key.
type PeerSessionKeyProvider interface {
	PeerSessionKey(host, version string, keyID byte) (key []byte, available bool, err error)
}

func SharedPeerSessionKey(store Storage, host, version string, keyID byte) ([]byte, bool, error) {
	if source, ok := store.(PeerSessionKeyProvider); ok {
		return source.PeerSessionKey(host, version, keyID)
	}
	return nil, false, nil
}

func loadPeerSessionSecret(ctx context.Context, pool *pgxpool.Pool) ([]byte, error) {
	candidate := make([]byte, 32)
	if _, err := rand.Read(candidate); err != nil {
		return nil, fmt.Errorf("generate peer session secret: %w", err)
	}
	defer clear(candidate)
	// First writer wins. Concurrent replicas must read the same stored key.
	if _, err := pool.Exec(ctx, `INSERT INTO devshard_peer_rpc_secret (singleton, secret)
		VALUES (TRUE, $1) ON CONFLICT (singleton) DO NOTHING`, candidate); err != nil {
		return nil, fmt.Errorf("initialize peer session secret: %w", err)
	}
	var secret []byte
	if err := pool.QueryRow(ctx, `SELECT secret FROM devshard_peer_rpc_secret WHERE singleton = TRUE`).Scan(&secret); err != nil {
		return nil, fmt.Errorf("read peer session secret: %w", err)
	}
	if len(secret) != 32 {
		return nil, fmt.Errorf("invalid stored peer session secret length %d", len(secret))
	}
	return secret, nil
}

func (s *Postgres) PeerSessionKey(host, version string, keyID byte) ([]byte, bool, error) {
	key, err := signing.DerivePeerSessionKeyFromSecret(s.peerSessionSecret, host, version, keyID)
	return key, true, err
}

func (h *HybridStorage) PeerSessionKey(host, version string, keyID byte) ([]byte, bool, error) {
	h.mu.RLock()
	pg, requiresPG := h.pg, h.preferPG || h.degradedOwnerOnly
	h.mu.RUnlock()
	if pg == nil && requiresPG {
		return nil, true, fmt.Errorf("peer session key: configured Postgres backend is unavailable")
	}
	return SharedPeerSessionKey(pg, host, version, keyID)
}

func (m *ManagedStorage) PeerSessionKey(host, version string, keyID byte) ([]byte, bool, error) {
	return SharedPeerSessionKey(m.inner, host, version, keyID)
}

func (g *ObsRepairGate) PeerSessionKey(host, version string, keyID byte) ([]byte, bool, error) {
	return SharedPeerSessionKey(g.Storage, host, version, keyID)
}
