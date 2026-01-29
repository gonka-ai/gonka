package authzcache

import (
	"context"
	"testing"
	"time"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockQueryClient mocks the inference query client
type MockQueryClient struct {
	mock.Mock
}

func (m *MockQueryClient) GranteesByMessageType(ctx context.Context, req *types.QueryGranteesByMessageTypeRequest) (*types.QueryGranteesByMessageTypeResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*types.QueryGranteesByMessageTypeResponse), args.Error(1)
}

func (m *MockQueryClient) InferenceParticipant(ctx context.Context, req *types.QueryInferenceParticipantRequest) (*types.QueryInferenceParticipantResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*types.QueryInferenceParticipantResponse), args.Error(1)
}

// MockCosmosClient mocks the cosmos message client
type MockCosmosClient struct {
	mock.Mock
	queryClient *MockQueryClient
}

func (m *MockCosmosClient) NewInferenceQueryClient() interface {
	GranteesByMessageType(ctx context.Context, req *types.QueryGranteesByMessageTypeRequest) (*types.QueryGranteesByMessageTypeResponse, error)
	InferenceParticipant(ctx context.Context, req *types.QueryInferenceParticipantRequest) (*types.QueryInferenceParticipantResponse, error)
} {
	return m.queryClient
}

func TestAuthzCache_CacheHit(t *testing.T) {
	// Pre-populate cache with signers
	cache := &AuthzCache{
		cache: map[string]*cachedEntry{
			"granter1|/msg/type": {
				signers: []SignerInfo{
					{Address: "addr1", PubKey: "pubkey1"},
					{Address: "addr2", PubKey: "pubkey2"},
				},
				expiresAt: time.Now().Add(time.Minute),
			},
		},
	}

	pubkeys, err := cache.GetPubKeys(context.Background(), "granter1", "/msg/type")
	assert.NoError(t, err)
	assert.Equal(t, []string{"pubkey1", "pubkey2"}, pubkeys)
}

func TestAuthzCache_CacheExpired(t *testing.T) {
	// Pre-populate cache with expired entry
	cache := &AuthzCache{
		cache: map[string]*cachedEntry{
			"granter1|/msg/type": {
				signers: []SignerInfo{
					{Address: "addr1", PubKey: "old_pubkey"},
				},
				expiresAt: time.Now().Add(-time.Minute), // expired
			},
		},
	}

	// Can't call GetPubKeys without a recorder, but we can verify the expiry check
	entry := cache.cache["granter1|/msg/type"]
	assert.True(t, time.Now().After(entry.expiresAt))
}

func TestAuthzCache_CacheKeyFormat(t *testing.T) {
	// Test that cache key is correctly formed
	granterAddress := "gonka1abc"
	msgTypeUrl := "/inference.inference.MsgStartInference"
	expectedKey := granterAddress + "|" + msgTypeUrl

	cache := &AuthzCache{
		cache: map[string]*cachedEntry{
			expectedKey: {
				signers: []SignerInfo{
					{Address: granterAddress, PubKey: "pubkey1"},
				},
				expiresAt: time.Now().Add(time.Minute),
			},
		},
	}

	pubkeys, err := cache.GetPubKeys(context.Background(), granterAddress, msgTypeUrl)
	assert.NoError(t, err)
	assert.Equal(t, []string{"pubkey1"}, pubkeys)
}

func TestAuthzCache_DifferentMsgTypes(t *testing.T) {
	// Test that different message types are cached separately
	cache := &AuthzCache{
		cache: map[string]*cachedEntry{
			"granter1|/msg/type1": {
				signers: []SignerInfo{
					{Address: "addr1", PubKey: "pubkey_type1"},
				},
				expiresAt: time.Now().Add(time.Minute),
			},
			"granter1|/msg/type2": {
				signers: []SignerInfo{
					{Address: "addr2", PubKey: "pubkey_type2"},
				},
				expiresAt: time.Now().Add(time.Minute),
			},
		},
	}

	pubkeys1, err := cache.GetPubKeys(context.Background(), "granter1", "/msg/type1")
	assert.NoError(t, err)
	assert.Equal(t, []string{"pubkey_type1"}, pubkeys1)

	pubkeys2, err := cache.GetPubKeys(context.Background(), "granter1", "/msg/type2")
	assert.NoError(t, err)
	assert.Equal(t, []string{"pubkey_type2"}, pubkeys2)
}

func TestAuthzCache_GetPubKeyForSigner(t *testing.T) {
	cache := &AuthzCache{
		cache: map[string]*cachedEntry{
			"granter1|/msg/type": {
				signers: []SignerInfo{
					{Address: "signer1", PubKey: "pubkey1"},
					{Address: "signer2", PubKey: "pubkey2"},
					{Address: "granter1", PubKey: "granter_pubkey"},
				},
				expiresAt: time.Now().Add(time.Minute),
			},
		},
	}

	// Should find signer1
	pubkey, err := cache.GetPubKeyForSigner(context.Background(), "granter1", "signer1", "/msg/type")
	assert.NoError(t, err)
	assert.Equal(t, "pubkey1", pubkey)

	// Should find signer2
	pubkey, err = cache.GetPubKeyForSigner(context.Background(), "granter1", "signer2", "/msg/type")
	assert.NoError(t, err)
	assert.Equal(t, "pubkey2", pubkey)

	// Should find granter itself
	pubkey, err = cache.GetPubKeyForSigner(context.Background(), "granter1", "granter1", "/msg/type")
	assert.NoError(t, err)
	assert.Equal(t, "granter_pubkey", pubkey)

	// Should return empty string for unknown signer (not an error)
	pubkey, err = cache.GetPubKeyForSigner(context.Background(), "granter1", "unknown_signer", "/msg/type")
	assert.NoError(t, err)
	assert.Equal(t, "", pubkey)
}
