package rpcserver

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/transport"
	"devshard/transport/rpcpb"
)

func TestPeerAuth_ReplicaAdmitsTheOtherChildsToken(t *testing.T) {
	host := testutil.MustGenerateKey(t)
	version := "v6"
	key, err := host.DerivePeerSessionKey(host.Address(), version, SessionKeyID)
	require.NoError(t, err)
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	cfg := PeerAuthConfig{
		SessionKey: append([]byte(nil), key...),
		Version:    version,
		KeyID:      SessionKeyID,
		Now:        clock.Now,
		SessionTTL: 30 * time.Second,
	}
	one := NewPeerAuthHandler(signing.NewSecp256k1Verifier(), host.Address(), cfg)
	two := NewPeerAuthHandler(signing.NewSecp256k1Verifier(), host.Address(), cfg)

	peer := testutil.MustGenerateKey(t)
	issued := attachOnHost(t, one, host.Address(), peer, []byte("replica-attach-nonce-aaaa"), clock.Now().Unix())
	got, ok := two.LookupToken(issued.SessionToken)
	require.True(t, ok)
	require.Equal(t, peer.Address(), got)

	otherVersion := NewPeerAuthHandler(signing.NewSecp256k1Verifier(), host.Address(), PeerAuthConfig{
		SessionKey: append([]byte(nil), key...),
		Version:    "v5",
		KeyID:      SessionKeyID,
		Now:        clock.Now,
	})
	_, ok = otherVersion.LookupToken(issued.SessionToken)
	require.False(t, ok)

	otherHost := testutil.MustGenerateKey(t).Address()
	wrongHost := NewPeerAuthHandler(signing.NewSecp256k1Verifier(), otherHost, cfg)
	_, ok = wrongHost.LookupToken(issued.SessionToken)
	require.False(t, ok)

	forged := append([]byte(nil), issued.SessionToken...)
	forged[len(forged)-1] ^= 0xff
	_, ok = two.LookupToken(forged)
	require.False(t, ok)

	clock.Advance(30*time.Second + sessionTokenSkew + time.Second)
	_, ok = two.LookupToken(issued.SessionToken)
	require.False(t, ok)
}

func attachOnHost(t *testing.T, auth *PeerAuthHandler, hostAddr string, signer *signing.Secp256k1Signer, nonce []byte, ts int64) *rpcpb.AttachResponse {
	t.Helper()
	sig, err := transport.SignAttach(signer, hostAddr, ts, signer.Address(), nonce, transport.AttachProtocolVersion, nil)
	require.NoError(t, err)
	resp, err := auth.Attach(WithEscrowID(context.Background(), testEscrowID), connect.NewRequest(&rpcpb.AttachRequest{
		PeerAddress:     signer.Address(),
		AttachNonce:     nonce,
		ProtocolVersion: transport.AttachProtocolVersion,
		HostAddress:     hostAddr,
		Timestamp:       ts,
		Signature:       sig,
	}))
	require.NoError(t, err)
	return resp.Msg
}
