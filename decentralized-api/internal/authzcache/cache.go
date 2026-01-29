package authzcache

import (
	"context"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"sync"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

const authzCacheTTL = 2 * time.Minute

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
// Uses caching to avoid repeated chain queries.
func (c *AuthzCache) getSigners(ctx context.Context, granterAddress, msgTypeUrl string) ([]SignerInfo, error) {
	cacheKey := granterAddress + "|" + msgTypeUrl

	c.mu.RLock()
	if entry, ok := c.cache[cacheKey]; ok && time.Now().Before(entry.expiresAt) {
		signers := entry.signers
		c.mu.RUnlock()
		return signers, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock
	if entry, ok := c.cache[cacheKey]; ok && time.Now().Before(entry.expiresAt) {
		return entry.signers, nil
	}

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
	participant, err := queryClient.InferenceParticipant(ctx, &types.QueryInferenceParticipantRequest{
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

	c.cache[cacheKey] = &cachedEntry{
		signers:   signers,
		expiresAt: time.Now().Add(authzCacheTTL),
	}

	logging.Debug("Cached authz signers", types.Validation,
		"granterAddress", granterAddress, "count", len(signers))

	return signers, nil
}
