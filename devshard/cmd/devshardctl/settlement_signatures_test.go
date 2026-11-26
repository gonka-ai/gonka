package main

import (
	"encoding/base64"
	"testing"

	"devshard/internal/testutil"
	"devshard/types"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func holdUnresolvableSignatures(t *testing.T, env *testProxyEnv, slots ...uint32) map[uint32][]byte {
	t.Helper()
	root, err := env.sm.ComputeStateRoot()
	require.NoError(t, err)
	content, err := proto.Marshal(&types.StateSignatureContent{StateRoot: root, EscrowId: env.proxy.escrowID, Nonce: env.session.Nonce()})
	require.NoError(t, err)
	held := make(map[uint32][]byte, len(slots))
	for _, slot := range slots {
		signature, err := testutil.MustGenerateKey(t).Sign(content)
		require.NoError(t, err)
		held[slot] = signature
	}
	env.session.Signatures()[env.session.Nonce()] = held
	return held
}

// Test flow:
//  1. Build a 3-slot proxy whose session has no warm-key resolver and hold, at its nonce, signatures by keys outside the group for all 3 slots: the session's re-check finds none valid.
//  2. Call `settlementJSON`.
//  3. Assert it succeeds and carries the 3 held signatures in slot order: they reach the chain's quorum of 3, which a warm key the chain has a grant for would pass.
func TestSettlementJSONPostsTheHeldSignaturesAtTheChainQuorum(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	held := holdUnresolvableSignatures(t, env, 2, 0, 1)

	got, err := env.proxy.settlementJSON()

	require.NoError(t, err)
	require.Equal(t, []SlotSignatureJSON{
		{SlotID: 0, Signature: base64.StdEncoding.EncodeToString(held[0])},
		{SlotID: 1, Signature: base64.StdEncoding.EncodeToString(held[1])},
		{SlotID: 2, Signature: base64.StdEncoding.EncodeToString(held[2])},
	}, got.Signatures)
}

// Test flow:
//  1. Build a 3-slot proxy whose session has no warm-key resolver and hold, at its nonce, signatures by keys outside the group for slots 0 and 2 only.
//  2. Call `settlementJSON`.
//  3. Assert it errors with no settlement: 2 slot signatures are below the chain's quorum of 3, so a settle could only fail.
func TestSettlementJSONPostsNothingBelowTheChainQuorum(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	holdUnresolvableSignatures(t, env, 0, 2)

	got, err := env.proxy.settlementJSON()

	require.ErrorContains(t, err, "2 of 3 slots signed, 3 needed")
	require.Equal(t, SettlementJSON{}, got)
}
