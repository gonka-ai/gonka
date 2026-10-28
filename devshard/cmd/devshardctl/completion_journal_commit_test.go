package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/storage"
	"devshard/stub"
	"devshard/user"

	"github.com/stretchr/testify/require"
)

// A Start's completion obligation is written with the Start's own diff, in the escrow's database, so a nonce costs
// the gateway one durable commit instead of one in gateway.db and another in the escrow's database.
func TestGatewayStartKeepsItsCompletionInTheEscrowDatabase(t *testing.T) {
	const escrowID = "42"
	signers := make([]*signing.Secp256k1Signer, 4)
	for i := range signers {
		signers[i] = testutil.MustGenerateKey(t)
	}
	creator := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(signers)
	verifier := signing.NewSecp256k1Verifier()
	config := testutil.DefaultConfig(4)
	sessionPath := filepath.Join(t.TempDir(), "escrow-"+escrowID)
	db, err := storage.NewSQLite(sessionPath)
	require.NoError(t, err)
	require.NoError(t, db.CreateSession(storage.CreateSessionParams{EscrowID: escrowID, Version: testutil.RuntimeTestVersion, CreatorAddr: creator.Address(), Config: config, Group: group, InitialBalance: 1000000}))
	clients := make([]user.HostClient, len(signers))
	for i := range clients {
		sm := statetest.MustStateMachine(t, escrowID, config, group, 1000000, creator.Address(), verifier)
		h, err := host.NewHost(sm, signers[i], stub.NewInferenceEngine(), escrowID, group, nil, host.WithGrace(10))
		require.NoError(t, err)
		clients[i] = &user.InProcessClient{Host: h}
	}
	sm := statetest.MustStateMachine(t, escrowID, config, group, 1000000, creator.Address(), verifier)
	session, err := user.NewSession(sm, creator, escrowID, group, clients, verifier, user.WithStorage(db))
	require.NoError(t, err)

	gatewayDB := filepath.Join(t.TempDir(), "gateway.db")
	journal, err := NewSQLiteGatewayStore(gatewayDB)
	require.NoError(t, err)
	g := &Gateway{store: journal}
	g.attachRuntimeSharedState(&devshardRuntime{id: escrowID, session: session})

	p, err := session.PrepareInference(context.Background(), defaultParams())
	require.NoError(t, err)
	inGateway, err := journal.HasInferenceCompletion(escrowID, p.Nonce())
	require.NoError(t, err)
	require.False(t, inGateway, "the Start's completion obligation went to gateway.db, a second durable commit for the nonce")
	require.NoError(t, journal.Close())
	require.NoError(t, db.Close())

	epochFiles, err := filepath.Glob(filepath.Join(sessionPath, "epoch_*.db"))
	require.NoError(t, err)
	require.Len(t, epochFiles, 1)
	raw, err := sql.Open("sqlite", epochFiles[0])
	require.NoError(t, err)
	defer raw.Close()
	var diffs, obligations int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM diffs WHERE escrow_id = ? AND nonce = ?`, escrowID, p.Nonce()).Scan(&diffs))
	require.Equal(t, 1, diffs)
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM inference_completions WHERE escrow_id = ? AND nonce = ?`, escrowID, p.Nonce()).Scan(&obligations))
	require.Equal(t, 1, obligations, "the obligation is not next to the Start's diff in the escrow's database")
}
