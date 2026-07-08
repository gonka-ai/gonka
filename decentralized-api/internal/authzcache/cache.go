package authzcache

import (
	"context"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"sync"
	"time"

	"github.com/productscience/inference/x/inference/types"
	"golang.org/x/sync/singleflight"
)

const authzCacheTTL = 2 * time.Minute

// authzQueryTimeout bounds the coalesced signer lookup. The queries run under a
// fresh context, not any single caller's, so one coalesced caller cancelling
// (client disconnect or its own deadline firing) does not fail every other
// caller that shares the singleflight result.
const authzQueryTimeout = 15 * time.Second

// SignerInfo holds address and pubkey for an authorized signer.
type SignerInfo struct {
	Address string
	PubKey  string
}

type cachedEntry struct {
	signers   []SignerInfo // all authorized signers (granter + grantees)
	expiresAt time.Time
}

// AuthzCache caches authorized signers for granter addresses to avoid repeated chain queries.
// Keys are cached with TTL since authz grants can change.
type AuthzCache struct {
	mu    sync.RWMutex
	cache map[string]*cachedEntry // "granterAddress|msgTypeUrl" -> entry
	// sf coalesces concurrent misses for the same key into a single pair of chain
	// queries, so an epoch-boundary/expiry burst does not fan out N identical
	// lookups; combined with running the queries outside c.mu it stops one slow
	// granter from throttling verification for every other granter.
	sf       singleflight.Group
	recorder cosmosclient.CosmosMessageClient
}

func NewAuthzCache(recorder cosmosclient.CosmosMessageClient) *AuthzCache {
	return &AuthzCache{
		cache:    make(map[string]*cachedEntry),
		recorder: recorder,
	}
}

// GetPubKeys returns all public keys authorized to sign on behalf of granterAddress.
// Includes granter's own key plus any grantee keys via authz.
// Results are cached with TTL.
func (c *AuthzCache) GetPubKeys(ctx context.Context, granterAddress, msgTypeUrl string) ([]string, error) {
	signers, err := c.getSigners(ctx, granterAddress, msgTypeUrl)
	if err != nil {
		return nil, err
	}

	pubkeys := make([]string, len(signers))
	for i, s := range signers {
		pubkeys[i] = s.PubKey
	}
	return pubkeys, nil
}

// GetPubKeyForSigner returns the pubkey for a specific signer address, if authorized.
// Returns empty string and no error if the signer is not authorized.
// This enables verifying signatures against a specific validator_signer_address.
func (c *AuthzCache) GetPubKeyForSigner(ctx context.Context, granterAddress, signerAddress, msgTypeUrl string) (string, error) {
	signers, err := c.getSigners(ctx, granterAddress, msgTypeUrl)
	if err != nil {
		return "", err
	}

	for _, s := range signers {
		if s.Address == signerAddress {
			return s.PubKey, nil
		}
	}

	return "", nil // not found, but not an error
}

// getSigners returns all authorized signers for the granter/msgType combination.
// Uses caching to avoid repeated chain queries.
func (c *AuthzCache) getSigners(ctx context.Context, granterAddress, msgTypeUrl string) ([]SignerInfo, error) {
	cacheKey := granterAddress + "|" + msgTypeUrl

	// Fast path: cache hit under the read lock.
	c.mu.RLock()
	if entry, ok := c.cache[cacheKey]; ok && time.Now().Before(entry.expiresAt) {
		signers := entry.signers
		c.mu.RUnlock()
		return signers, nil
	}
	c.mu.RUnlock()

	// Miss: coalesce concurrent fetches for this key into one pair of RPCs, run
	// OUTSIDE c.mu so a slow granter cannot throttle verification for everyone.
	// DoChan (not Do) so this caller can still abandon its wait via its own ctx
	// without killing the shared query for everyone else.
	ch := c.sf.DoChan(cacheKey, func() (interface{}, error) {
		// Re-check: another goroutine may have populated the cache while we were
		// becoming the singleflight leader.
		c.mu.RLock()
		if entry, ok := c.cache[cacheKey]; ok && time.Now().Before(entry.expiresAt) {
			signers := entry.signers
			c.mu.RUnlock()
			return signers, nil
		}
		c.mu.RUnlock()

		logging.Debug("Fetching authz signers", types.Validation,
			"granterAddress", granterAddress, "msgTypeUrl", msgTypeUrl)

		queryClient := c.recorder.NewInferenceQueryClient()

		// Run under a fresh, decoupled context so a coalesced caller's
		// cancellation cannot fail everyone sharing this singleflight result.
		qctx, cancel := context.WithTimeout(context.Background(), authzQueryTimeout)
		defer cancel()

		// Get grantees (warm keys) for this message type
		grantees, err := queryClient.GranteesByMessageType(qctx, &types.QueryGranteesByMessageTypeRequest{
			GranterAddress: granterAddress,
			MessageTypeUrl: msgTypeUrl,
		})
		if err != nil {
			return nil, err
		}

		// Get granter's own public key
		participant, err := queryClient.AccountByAddress(qctx, &types.QueryAccountByAddressRequest{
			Address: granterAddress,
		})
		if err != nil {
			return nil, err
		}

		// Collect all signers: grantees + granter
		signers := make([]SignerInfo, 0, len(grantees.Grantees)+1)
		for _, grantee := range grantees.Grantees {
			signers = append(signers, SignerInfo{
				Address: grantee.Address,
				PubKey:  grantee.PubKey,
			})
		}
		signers = append(signers, SignerInfo{
			Address: granterAddress,
			PubKey:  participant.Pubkey,
		})

		c.mu.Lock()
		c.cache[cacheKey] = &cachedEntry{
			signers:   signers,
			expiresAt: time.Now().Add(authzCacheTTL),
		}
		c.mu.Unlock()

		logging.Debug("Cached authz signers", types.Validation,
			"granterAddress", granterAddress, "count", len(signers))

		return signers, nil
	})

	select {
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		return res.Val.([]SignerInfo), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
