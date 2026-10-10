package rpcserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
)

func TestRPCTraffic_GetSignaturesCounted(t *testing.T) {
	now := time.Unix(1_710_000_000, 0)
	clock := now
	env := startLimitEnv(t, PeerAuthConfig{Now: func() time.Time { return clock }}, stubLookup{core: stubCore{}})
	require.NoError(t, env.getSigs())

	clock = now.Add(time.Minute)
	snap := env.auth.Traffic().Snapshot(clock)
	require.Equal(t, now.Unix(), snap.MinuteUnix)
	var sigs uint64
	for _, e := range snap.Host.Endpoints {
		if e.Endpoint == "GetSignatures" && e.Zone == transport.RPCZoneShared {
			sigs = e.Requests
		}
	}
	require.Equal(t, uint64(1), sigs)
	require.Equal(t, uint64(1), snap.Host.Attach.Attempts)
	require.Empty(t, snap.Shards, "nil LiveSession must not grow shard rows from the URL")
}

const testEscrowHeader = "X-Devshard-Test-Escrow"

type escrowHeaderRT struct {
	base http.RoundTripper
	id   string
}

func (t escrowHeaderRT) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	if t.id != "" {
		req.Header.Set(testEscrowHeader, t.id)
	}
	return t.base.RoundTrip(req)
}

func withEscrowFromHeader(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(testEscrowHeader)
		if id == "" {
			id = testEscrowID
		}
		h.ServeHTTP(w, r.WithContext(WithEscrowID(r.Context(), id)))
	})
}

func TestRPCTraffic_ShardRowsOnlyForLiveSession(t *testing.T) {
	now := time.Unix(1_710_000_000, 0)
	clock := now
	var existing int
	lookup := countingLookup{core: stubCore{}, n: &existing}
	auth := newTestAuth(PeerAuthConfig{
		Now:         func() time.Time { return clock },
		LiveSession: func(id string) bool { return id == testEscrowID },
	})
	srv := httptest.NewServer(withEscrowFromHeader(NewMux(auth, NewSessionHandler(lookup))))
	t.Cleanup(srv.Close)

	signer := testutil.MustGenerateKey(t)
	authc := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)
	attached := attachAt(t, authc, signer, []byte("traffic-live-escrow-nonce-01"), now.Unix())

	live := rpcpbconnect.NewSessionServiceClient(srv.Client(), srv.URL)
	_, err := live.GetSignatures(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}), attached.SessionToken))
	require.NoError(t, err)
	require.Equal(t, 1, existing, "LiveSession must not call SessionServerExisting")

	const fakeN = 20
	for i := 0; i < fakeN; i++ {
		id := strconv.Itoa(10_000 + i)
		cli := rpcpbconnect.NewSessionServiceClient(&http.Client{
			Transport: escrowHeaderRT{base: srv.Client().Transport, id: id},
		}, srv.URL)
		_, err := cli.GetSignatures(context.Background(), withSession(
			connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}), attached.SessionToken))
		require.NoError(t, err, "stub lookup ignores id; shard gate is LiveSession")
	}
	require.Equal(t, 1+fakeN, existing)

	clock = now.Add(time.Minute)
	snap := auth.Traffic().Snapshot(clock)
	require.Equal(t, uint64(1+1+fakeN), snap.Host.Requests, "Attach + live GetSignatures + fake URLs still count on the host")
	require.Len(t, snap.Shards, 1)
	require.Equal(t, testEscrowID, snap.Shards[0].EscrowID)
	require.Equal(t, uint64(1), snap.Shards[0].Requests)
}
