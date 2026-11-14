package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func TestGatewayStartVersion_RequiresCreatorSignedStart(t *testing.T) {
	user := testutil.MustGenerateKey(t)
	other := testutil.MustGenerateKey(t)
	const escrowID = "escrow-1"
	diff := testutil.SignDiff(t, user, escrowID, 1, []*types.DevshardTx{testutil.StartTxVersioned(1, testutil.RuntimeTestVersion)})
	v, err := GatewayStartVersion(signing.NewSecp256k1Verifier(), user.Address(), escrowID, []types.Diff{diff})
	require.NoError(t, err)
	require.Equal(t, testutil.RuntimeTestVersion, v)

	_, err = GatewayStartVersion(signing.NewSecp256k1Verifier(), other.Address(), escrowID, []types.Diff{diff})
	require.ErrorIs(t, err, types.ErrInvalidUserSig)

	_, err = GatewayStartVersion(signing.NewSecp256k1Verifier(), user.Address(), escrowID, nil)
	require.ErrorIs(t, err, types.ErrStartProofMissing)
}

func TestGatewayStartVersion_MissingVersion(t *testing.T) {
	user := testutil.MustGenerateKey(t)
	const escrowID = "escrow-1"
	diff := testutil.SignDiff(t, user, escrowID, 1, []*types.DevshardTx{testutil.StartTx(1)})
	_, err := GatewayStartVersion(signing.NewSecp256k1Verifier(), user.Address(), escrowID, []types.Diff{diff})
	require.ErrorIs(t, err, types.ErrStartProofMissing)
}

func TestGatewayStartVersion_ConflictingVersions(t *testing.T) {
	user := testutil.MustGenerateKey(t)
	const escrowID = "escrow-1"
	d1 := testutil.SignDiff(t, user, escrowID, 1, []*types.DevshardTx{testutil.StartTxVersioned(1, "v5")})
	d2 := testutil.SignDiff(t, user, escrowID, 2, []*types.DevshardTx{testutil.StartTxVersioned(2, "v4")})
	_, err := GatewayStartVersion(signing.NewSecp256k1Verifier(), user.Address(), escrowID, []types.Diff{d1, d2})
	require.ErrorIs(t, err, types.ErrProtocolVersionMismatch)
}
