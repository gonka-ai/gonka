package types

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBeaconTxCodec(t *testing.T) {
	sigma := &InjectedBeacon{Outcome: &InjectedBeacon_Sigma{Sigma: &BeaconSigma{Round: 7, KeyEpoch: 2, Sigma: bytes.Repeat([]byte{1}, 48)}}}
	tx, err := EncodeBeaconTx(sigma)
	require.NoError(t, err)
	require.True(t, IsBeaconTx(tx))
	got, err := DecodeBeaconTx(tx)
	require.NoError(t, err)
	require.Equal(t, uint64(7), got.GetSigma().Round)
	require.Nil(t, got.GetMiss())

	miss := &InjectedBeacon{Outcome: &InjectedBeacon_Miss{Miss: &BeaconMissProof{Round: 8, KeyEpoch: 2, ExtendedCommit: []byte{1, 2}}}}
	tx, err = EncodeBeaconTx(miss)
	require.NoError(t, err)
	got, err = DecodeBeaconTx(tx)
	require.NoError(t, err)
	require.Equal(t, []byte{1, 2}, got.GetMiss().ExtendedCommit)

	_, err = EncodeBeaconTx(&InjectedBeacon{})
	require.Error(t, err, "no outcome")
	_, err = DecodeBeaconTx([]byte("not a beacon tx"))
	require.Error(t, err)
	_, err = DecodeBeaconTx(BeaconTxPrefix)
	require.Error(t, err, "prefix without an outcome")
	_, err = DecodeBeaconTx(append(append([]byte(nil), BeaconTxPrefix...), 0xff, 0xff, 0xff))
	require.Error(t, err, "garbage after the prefix")
	_, err = DecodeBeaconTx(append(append([]byte(nil), BeaconTxPrefix...), make([]byte, MaxBeaconTxBytes)...))
	require.Error(t, err, "oversized")
	require.False(t, IsBeaconTx([]byte("GONKA_BEACON")))
}
