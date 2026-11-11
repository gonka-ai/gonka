package blsutil

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/stretchr/testify/require"
	blst "github.com/supranational/blst/bindings/go"
)

// m_h test vectors, produced by an independent implementation of the plan's
// formula sha256("GONKA_BLOCK_BEACON_V1" ‖ chain_id ‖ u64be(key_epoch) ‖
// u64be(h) ‖ blockHash(h-1)). Any change here is a consensus change.
func TestBeaconMessageVectors(t *testing.T) {
	zero := make([]byte, 32)
	seq := make([]byte, 32)
	for i := range seq {
		seq[i] = byte(i)
	}
	for _, v := range []struct {
		chain        string
		epoch, round uint64
		prev         []byte
		want         string
	}{
		{"gonka-mainnet", 1, 1, zero, "b0c4b50d825f721d930e7de7eb3ddeeec4ce87c43dc2157ad6239c9296a1a915"},
		{"gonka-mainnet", 42, 6971589, seq, "c4937545700bd9edb14c1c3956a8490a6108e32929bfd0dac21698cea67c5117"},
		{"gonka-testnet-2", 0xFFFFFFFFFFFFFFFF, 0x0102030405060708, seq, "de54169f724266e3bd3349fe37a7d39911ef3ef6ae496e4ac04acd9fc9a07a03"},
	} {
		got, err := BeaconMessage(v.chain, v.epoch, v.round, v.prev)
		require.NoError(t, err)
		require.Equal(t, v.want, hex.EncodeToString(got[:]), "%s/%d/%d", v.chain, v.epoch, v.round)
	}
}

func TestBeaconMessageInputs(t *testing.T) {
	prev := bytes.Repeat([]byte{7}, 32)
	_, err := BeaconMessage("", 1, 1, prev)
	require.Error(t, err, "empty chain id")
	for _, n := range []int{0, 31, 33, 64} {
		_, err = BeaconMessage("c", 1, 1, make([]byte, n))
		require.Error(t, err, "prev hash of %d bytes", n)
	}

	// Every field is bound: changing any one changes m_h.
	base, err := BeaconMessage("chain", 3, 10, prev)
	require.NoError(t, err)
	other := map[string]func() ([32]byte, error){
		"chain": func() ([32]byte, error) { return BeaconMessage("chain2", 3, 10, prev) },
		"epoch": func() ([32]byte, error) { return BeaconMessage("chain", 4, 10, prev) },
		"round": func() ([32]byte, error) { return BeaconMessage("chain", 3, 11, prev) },
		"hash": func() ([32]byte, error) {
			p := append([]byte(nil), prev...)
			p[31] ^= 1
			return BeaconMessage("chain", 3, 10, p)
		},
	}
	for name, f := range other {
		m, err := f()
		require.NoError(t, err)
		require.NotEqual(t, base, m, name)
	}
}

// HashToG1 regression vector: the mapping threshold signing already uses.
func TestHashToG1Vector(t *testing.T) {
	msg, _ := hex.DecodeString("b0c4b50d825f721d930e7de7eb3ddeeec4ce87c43dc2157ad6239c9296a1a915")
	p, err := HashToG1(msg)
	require.NoError(t, err)
	b := p.Bytes()
	require.Equal(t, "92f15760e50d85ba98a0c94eea938ca48c3d7681750e0b9f1ea1e265b099c092e13b24970161832c44c2bebc238721d1", hex.EncodeToString(b[:]))
	require.True(t, p.IsInSubGroup())

	_, err = HashToG1(make([]byte, 31))
	require.Error(t, err)
	_, err = HashToG1Blst(make([]byte, 33))
	require.Error(t, err)
}

// keypair returns a random scalar (little-endian, blst order) and its
// compressed G2 public key.
func keypair(t *testing.T) ([ScalarLen]byte, []byte) {
	t.Helper()
	var e fr.Element
	_, err := e.SetRandom()
	require.NoError(t, err)
	be := e.Bytes()
	var le [ScalarLen]byte
	for i := range be {
		le[i] = be[ScalarLen-1-i]
	}
	return le, blst.P2Generator().Mult(le[:], 255).ToAffine().Compress()
}

func TestSignAndVerifyPartials(t *testing.T) {
	msg, err := BeaconMessage("gonka-test", 5, 100, bytes.Repeat([]byte{1}, 32))
	require.NoError(t, err)
	const n = 6
	scalars := make([][ScalarLen]byte, n)
	pks := make([][]byte, n)
	for i := range scalars {
		scalars[i], pks[i] = keypair(t)
	}
	sigs, err := SignPartials(msg[:], scalars)
	require.NoError(t, err)
	require.Len(t, sigs, n*SignatureLen)

	for i := 0; i < n; i++ {
		sig := sigs[i*SignatureLen : (i+1)*SignatureLen]
		require.True(t, VerifyPartial(msg[:], sig, pks[i]), "slot %d", i)
		require.False(t, VerifyPartial(msg[:], sig, pks[(i+1)%n]), "slot %d under another slot's key", i)
	}

	sig0 := append([]byte(nil), sigs[:SignatureLen]...)
	flipped := append([]byte(nil), sig0...)
	flipped[SignatureLen-1] ^= 1
	require.False(t, VerifyPartial(msg[:], flipped, pks[0]), "bit flip")

	other := msg
	other[0] ^= 1
	require.False(t, VerifyPartial(other[:], sig0, pks[0]), "different message")

	require.False(t, VerifyPartial(msg[:], sig0[:47], pks[0]), "short signature")
	require.False(t, VerifyPartial(msg[:], sig0, pks[0][:95]), "short key")
	require.False(t, VerifyPartial(msg[:31], sig0, pks[0]), "short message")

	// The identity point is never a valid partial.
	inf := make([]byte, SignatureLen)
	inf[0] = 0xc0
	require.False(t, VerifyPartial(msg[:], inf, pks[0]), "identity signature")

	// Deterministic: same inputs, same bytes (BLS signatures are unique).
	again, err := SignPartials(msg[:], scalars)
	require.NoError(t, err)
	require.Equal(t, sigs, again)

	empty, err := SignPartials(msg[:], nil)
	require.NoError(t, err)
	require.Empty(t, empty)
	_, err = SignPartials(msg[:16], scalars)
	require.Error(t, err)
}
