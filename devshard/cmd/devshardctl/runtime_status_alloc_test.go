package main

import (
	"testing"

	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
	"devshard/user"

	"github.com/stretchr/testify/require"
)

// /v1/status is auth-exempt and reads only the balance and the protocol tag of each escrow.
// Copying the whole escrow state to get them costs one allocation per live inference, per call.
func TestRuntimeStatusDoesNotCopyTheInferenceMap(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	verifier := signing.NewSecp256k1Verifier()
	sm := statetest.MustStateMachine(t, "escrow-status", testutil.DefaultConfig(len(hosts)), group, 1_000_000, userKey.Address(), verifier)

	st := sm.ExportState()
	st.Balance = 777
	if st.Inferences == nil {
		st.Inferences = make(map[uint64]*types.InferenceRecord)
	}
	for id := uint64(1); id <= 5_000; id++ {
		st.Inferences[id] = &types.InferenceRecord{Status: types.StatusPending, Model: "m", PromptHash: []byte("p")}
	}
	require.NoError(t, sm.RestoreState(st))

	session, err := user.NewSession(sm, userKey, "escrow-status", group, make([]user.HostClient, len(group)), verifier)
	require.NoError(t, err)
	rt := &devshardRuntime{id: "escrow-status", model: "m", proxy: &Proxy{sm: sm, session: session}}

	status := rt.snapshot()
	require.Equal(t, uint64(777), status.Balance)
	require.Equal(t, st.StateRootAndProtocolVersion, status.SessionVersion)

	allocs := testing.AllocsPerRun(20, func() { _ = rt.snapshot() })
	require.Less(t, allocs, 100.0, "runtime status allocates per live inference")
}
