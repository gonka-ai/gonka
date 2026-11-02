package authzcache

import (
	"common/logging"
	"context"
	"decentralized-api/cosmosclient"
	"sync"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	authzCacheTTL = 2 * time.Minute
	// authzCacheErrorTTL bounds how often a failing granter lookup reaches the chain.
	authzCacheErrorTTL = 5 * time.Second
)

// SignerInfo holds address and pubkey for an authorized signer.
type SignerInfo struct {
	Address string
	PubKey  string
}

type cachedEntry struct {
	signers   []SignerInfo // all authorized signers (granter + grantees)
	err       error        // failed lookup, served until expiresAt
	expiresAt time.Time
}

// AuthzCache caches authorized signers for granter addresses to avoid repeated chain queries.
// Keys are cached with TTL since authz grants can change.
type AuthzCache struct {
	mu       sync.RWMutex
	cache    map[string]*cachedEntry // "granterAddress|msgTypeUrl" -> entry
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
// Uses caching to avoid repeated chain queries. The chain is queried without holding
// the lock, so concurrent misses for the same key may each query it.
func (c *AuthzCache) getSigners(ctx context.Context, granterAddress, msgTypeUrl string) ([]SignerInfo, error) {
	cacheKey := granterAddress + "|" + msgTypeUrl

	c.mu.RLock()
	if entry, ok := c.cache[cacheKey]; ok && time.Now().Before(entry.expiresAt) {
		signers, err := entry.signers, entry.err
		c.mu.RUnlock()
		return signers, err
	}
	c.mu.RUnlock()

	signers, err := c.fetchSigners(ctx, granterAddress, msgTypeUrl)
	if err != nil {
		// A canceled caller or an unknown address says nothing about the granter, and
		// caching unknown or malformed addresses would let arbitrary input grow the map.
		if _, addrErr := sdk.AccAddressFromBech32(granterAddress); addrErr == nil && ctx.Err() == nil && status.Code(err) != codes.NotFound {
			c.mu.Lock()
			// Drop expired entries so failures for many distinct addresses stay bounded
			// by the error TTL window.
			now := time.Now()
			for key, entry := range c.cache {
				if !now.Before(entry.expiresAt) {
					delete(c.cache, key)
				}
			}
			c.cache[cacheKey] = &cachedEntry{err: err, expiresAt: now.Add(authzCacheErrorTTL)}
			c.mu.Unlock()
		}
		return nil, err
	}

	c.mu.Lock()
	c.cache[cacheKey] = &cachedEntry{
		signers:   signers,
		expiresAt: time.Now().Add(authzCacheTTL),
	}
	c.mu.Unlock()

	logging.Debug("Cached authz signers", types.Validation,
		"granterAddress", granterAddress, "count", len(signers))

	return signers, nil
}

func (c *AuthzCache) fetchSigners(ctx context.Context, granterAddress, msgTypeUrl string) ([]SignerInfo, error) {
	logging.Debug("Fetching authz signers", types.Validation,
		"granterAddress", granterAddress, "msgTypeUrl", msgTypeUrl)

	queryClient := c.recorder.NewInferenceQueryClient()

	// Get grantees (warm keys) for this message type
	grantees, err := queryClient.GranteesByMessageType(ctx, &types.QueryGranteesByMessageTypeRequest{
		GranterAddress: granterAddress,
		MessageTypeUrl: msgTypeUrl,
	})
	if err != nil {
		return nil, err
	}

	// Get granter's own public key
	participant, err := queryClient.AccountByAddress(ctx, &types.QueryAccountByAddressRequest{
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

	return signers, nil
}
