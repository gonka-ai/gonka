package keeper

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/sha3"

	"github.com/productscience/inference/x/bls/types"
)

func keccak256(parts ...[]byte) []byte {
	h := sha3.NewLegacyKeccak256()
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

func TestTransitionOperationTag_PinnedValue(t *testing.T) {
	tag := types.TransitionOperationTag()
	require.Len(t, tag, 32)
	require.Equal(t, keccak256([]byte("TRANSITION_OPERATION")), tag,
		`TransitionOperationTag must equal keccak256("TRANSITION_OPERATION")`)

	// Returned slice must be a copy: mutating it must not corrupt the package value.
	tag[0] ^= 0xff
	require.Equal(t, keccak256([]byte("TRANSITION_OPERATION")), types.TransitionOperationTag(),
		"TransitionOperationTag must return a fresh copy each call")
}

func TestRotationMessage_DomainSeparated(t *testing.T) {
	k := Keeper{}

	const epoch = uint64(314)
	chainID := make([]byte, 32) // GONKA_CHAIN_ID (32 bytes)
	for i := range chainID {
		chainID[i] = 0xAB
	}

	tag := types.TransitionOperationTag()

	buildRotation := func(withTag bool, key []byte) []byte {
		ep := make([]byte, 8)
		binary.BigEndian.PutUint64(ep, epoch)
		b := append([]byte{}, ep...)
		b = append(b, chainID...)
		if withTag {
			b = append(b, tag...)
		}
		b = append(b, key...)
		return b
	}

	nsReqId := keccak256([]byte("attacker-creator"), []byte("attacker-request-id"))
	domain := make([]byte, 32)
	for i := range domain {
		domain[i] = 0xCD
	}
	data := [][]byte{domain}
	for i := 0; i < 6; i++ { // 1 domain + 6 chunks + nsReqId slot = 256 bytes after epoch||chainId
		chunk := make([]byte, 32)
		for j := range chunk {
			chunk[j] = byte(0x10 + i)
		}
		data = append(data, chunk)
	}
	external := k.encodeSigningData(types.SigningData{
		CurrentEpochId: epoch,
		ChainId:        chainID,
		RequestId:      nsReqId,
		Data:           data,
	})

	// The "key" the attacker tries to install is everything after epoch||chainId.
	forgedKey := external[40:] // nsReqId(32) || domain(32) || 6*32 data = 256 bytes
	require.Len(t, forgedKey, 256)

	require.Equal(t, external, buildRotation(false, forgedKey),
		"control: pre-fix (untagged) rotation must collide with the external request")
	newRotation := buildRotation(true, forgedKey)
	require.Equal(t, tag, newRotation[40:72], "TRANSITION_OPERATION tag must sit at offset 40")
	require.NotEqual(t, newRotation, external,
		"post-fix rotation must NOT collide with an external threshold-signing message")
	require.NotEqual(t, tag, external[40:72],
		"external request-id slot must not equal the transition tag")
}
