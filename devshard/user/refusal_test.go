package user

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func TestBuildRefusalPackageRetentionAndExpiry(t *testing.T) {
	for _, persisted := range []bool{false, true} {
		t.Run(fmt.Sprint(persisted), func(t *testing.T) {
			var s *Session
			var hosts []*signing.Secp256k1Signer
			if persisted {
				s, _, _, hosts, _ = buildLiveSession(t, 3, newTestStore(t))
			} else {
				s, hosts, _ = setupSession(t, 3, 100000, 100)
			}
			var err error
			_, err = s.BuildRefusalPackage(1)
			require.Error(t, err)
			s.mu.Lock()
			_, _, err = s.composeDiffLocked([]*types.DevshardTx{testutil.StartTx(1)})
			require.NoError(t, err)
			s.mu.Unlock()
			require.Eventually(t, func() bool {
				s.mu.Lock()
				defer s.mu.Unlock()
				return s.refusalCandidate != nil && s.refusalCandidate.data != nil
			}, time.Second, time.Millisecond)
			s.mu.Lock()
			for slot, signer := range hosts {
				if slot > 0 {
					_, _, err = s.composeDiffLocked(nil)
					require.NoError(t, err)
				}
				root, err := s.sm.ComputeStateRoot()
				require.NoError(t, err)
				data, err := proto.Marshal(&types.StateSignatureContent{EscrowId: s.escrowID, Nonce: s.nonce, StateRoot: root})
				require.NoError(t, err)
				sig, err := signer.Sign(data)
				require.NoError(t, err)
				require.NoError(t, s.verifyStateSignature(s.nonce, root, sig, signer.Address()))
				s.retainRefusalSignatureLocked(s.nonce, uint32(slot), sig)
			}
			// The union reaches quorum, but neither set does.
			require.Nil(t, s.refusalUsable)
			_, _, err = s.composeDiffLocked(nil)
			require.NoError(t, err)
			root, err := s.sm.ComputeStateRoot()
			require.NoError(t, err)
			content, err := proto.Marshal(&types.StateSignatureContent{EscrowId: s.escrowID, Nonce: s.nonce, StateRoot: root})
			require.NoError(t, err)
			sig, err := hosts[0].Sign(content)
			require.NoError(t, err)
			s.retainRefusalSignatureLocked(s.nonce, 0, sig)
			s.mu.Unlock()
			p, err := s.BuildRefusalPackage(1)
			require.NoError(t, err)
			require.Equal(t, uint64(1), p.N)
			require.Equal(t, uint64(4), p.T)
			require.Len(t, p.Diffs, 3)
			verified, err := s.sm.VerifyRefusalPackage(p)
			require.NoError(t, err)
			require.Equal(t, p.T, verified.LatestNonce)
			p2, err := s.BuildRefusalPackage(1)
			require.NoError(t, err)
			require.True(t, &p.Snapshot[0] == &p2.Snapshot[0])
			p.Diffs[0].UserSig[0] ^= 1
			require.NotEqual(t, p.Diffs[0].UserSig, p2.Diffs[0].UserSig)
			s.mu.Lock()
			for s.nonce < 1002 {
				_, _, err = s.composeDiffLocked(nil)
				require.NoError(t, err)
			}
			s.mu.Unlock()
			_, err = s.BuildRefusalPackage(1)
			require.Error(t, err)
			// Missing proof never reaches verifier fan-out.
			_, err = s.CollectTimeoutVotes(context.Background(), 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, nil, nil, nil)
			require.Error(t, err)
		})
	}
}

func TestBuildRefusalPackageFirstDiff(t *testing.T) {
	s, hosts, _ := setupSession(t, 3, 100000, 100)
	prepared, err := s.PrepareInference(InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err)
	require.Equal(t, uint64(1), prepared.Nonce())
	root, err := s.sm.ComputeStateRoot()
	require.NoError(t, err)
	content, err := proto.Marshal(&types.StateSignatureContent{EscrowId: s.escrowID, Nonce: 1, StateRoot: root})
	require.NoError(t, err)
	for slot, signer := range hosts {
		sig, err := signer.Sign(content)
		require.NoError(t, err)
		require.NoError(t, s.ProcessResponse(slot, &host.HostResponse{Nonce: 1, StateHash: root, StateSig: sig}, 1))
	}
	require.Eventually(t, func() bool {
		_, err := s.BuildRefusalPackage(1)
		return err == nil
	}, time.Second, time.Millisecond)
	p, err := s.BuildRefusalPackage(1)
	require.NoError(t, err)
	require.Equal(t, uint64(1), p.N)
	require.Equal(t, p.N, p.T)
	require.Empty(t, p.Diffs)
	_, err = s.sm.VerifyRefusalPackage(p)
	require.NoError(t, err)
}
