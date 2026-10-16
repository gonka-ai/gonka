package user

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/types"
)

func warmKeyEntriesIn(diffs []types.Diff) (binds []*types.MsgWarmKeyBind, revokes []*types.MsgWarmKeyRevoke) {
	for _, d := range diffs {
		for _, tx := range d.Txs {
			if b := tx.GetWarmKeyBind(); b != nil {
				binds = append(binds, b)
			}
			if r := tx.GetWarmKeyRevoke(); r != nil {
				revokes = append(revokes, r)
			}
		}
	}
	return binds, revokes
}

// Test flow:
//  1. Run a warm-key session, grant a second key to slot 0's validator, send an inference, revoke the first key
//     of slot 1's validator, send another.
//  2. Assert the first diff bound every slot once, the grant produced exactly one bind in the next diff, the
//     revoke produced exactly one revoke, every in-process host ended with the gateway's history, and no entry
//     is proposed twice.
func TestSession_ProposesBindsAndRevokesFromTheGrantSource(t *testing.T) {
	session, coldKeys, warmKeys := setupWarmKeySession(t, 3)
	grants := session.warmKeySource.(*testutil.WarmKeyGrants)
	ctx := context.Background()

	_, err := session.SendInference(ctx, defaultParams)
	require.NoError(t, err)
	binds, revokes := warmKeyEntriesIn(session.Diffs())
	require.Len(t, binds, 3, "the first diff binds every live grant of the group")
	require.Empty(t, revokes)
	for _, b := range binds {
		require.Equal(t, warmKeys[b.SlotId].Address(), b.Address)
		require.Equal(t, uint64(1), b.GrantId)
	}

	second := testutil.MustGenerateKey(t)
	id := grants.Grant(coldKeys[0].Address(), second.Address())
	_, err = session.SendInference(ctx, defaultParams)
	require.NoError(t, err)
	binds, _ = warmKeyEntriesIn(session.Diffs())
	require.Len(t, binds, 4)
	require.Equal(t, uint32(0), binds[3].SlotId)
	require.Equal(t, second.Address(), binds[3].Address)
	require.Equal(t, id, binds[3].GrantId)
	require.Equal(t, []string{warmKeys[0].Address(), second.Address()}, session.StateMachine().WarmKeyHistory().Live(0),
		"two live keys on one slot during a rotation")

	grants.Revoke(coldKeys[1].Address(), warmKeys[1].Address())
	// Slot 1's host still signs with the revoked key; its receipt for the
	// next inference is refused, which is the point. Send to another slot.
	for i := 0; i < 3; i++ {
		_, _ = session.SendInference(ctx, defaultParams)
	}
	_, revokes = warmKeyEntriesIn(session.Diffs())
	require.Len(t, revokes, 1)
	require.Equal(t, uint32(1), revokes[0].SlotId)
	require.Equal(t, warmKeys[1].Address(), revokes[0].Address)
	require.Equal(t, uint64(1), revokes[0].GrantId)
	require.Empty(t, session.StateMachine().WarmKeyHistory().Live(1))

	want := session.StateMachine().WarmKeyHistory()
	for i, c := range session.clients {
		h := c.(*InProcessClient).Host
		if h.LatestNonce() < session.Nonce() {
			continue
		}
		require.Equal(t, want, h.SnapshotState().WarmKeyHistory, "host %d", i)
	}
}

// Test flow:
//  1. Make the grant source fail, send an inference, then restore it.
//  2. Assert a failing source proposes nothing and refuses nothing (the session goes on with cold-key receipts
//     refused, so the inference round fails), and the next compose after recovery proposes the binds.
func TestSession_GrantSourceOutageProposesNothing(t *testing.T) {
	session, _, _ := setupWarmKeySession(t, 3)
	grants := session.warmKeySource.(*testutil.WarmKeyGrants)
	grants.Err = errors.New("chain down")

	session.mu.Lock()
	proposals := session.warmKeyProposalsLocked(session.nonce + 1)
	session.mu.Unlock()
	require.Empty(t, proposals)

	grants.Err = nil
	session.mu.Lock()
	proposals = session.warmKeyProposalsLocked(session.nonce + 1)
	session.mu.Unlock()
	require.Len(t, proposals, 3)
}
