// Package blsutil holds the pure BLS primitives of the block beacon
// (devshard/docs/proposals/block-bls-beacon-vote-extensions-plan.md): the
// per-round message, the message-to-G1 map, and per-slot partial signing and
// verification. It has no keeper or app dependencies, so the chain, the dAPI
// and tools compute exactly the same bytes.
package blsutil

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fp"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/hash_to_curve"
	blst "github.com/supranational/blst/bindings/go"
)

const (
	// BeaconDomainTag separates beacon messages from every other BLS message.
	BeaconDomainTag = "GONKA_BLOCK_BEACON_V1"
	// MessageLen is the size of a beacon message and of any hashed message
	// mapped to G1 here.
	MessageLen = 32
	// BlockHashLen is the CometBFT block hash size.
	BlockHashLen = 32
	// SignatureLen is a compressed G1 point.
	SignatureLen = 48
	// ScalarLen is one fr element.
	ScalarLen = 32
)

// BeaconMessage returns m_h = sha256(tag ‖ chain_id ‖ u64be(key_epoch) ‖
// u64be(round) ‖ prevBlockHash). Everything after chain_id has a fixed size,
// so the encoding is unambiguous. prevBlockHash is the hash of block round-1.
func BeaconMessage(chainID string, keyEpoch, round uint64, prevBlockHash []byte) ([MessageLen]byte, error) {
	if chainID == "" {
		return [MessageLen]byte{}, errors.New("blsutil: empty chain id")
	}
	if len(prevBlockHash) != BlockHashLen {
		return [MessageLen]byte{}, fmt.Errorf("blsutil: previous block hash must be %d bytes, got %d", BlockHashLen, len(prevBlockHash))
	}
	h := sha256.New()
	h.Write([]byte(BeaconDomainTag))
	h.Write([]byte(chainID))
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], keyEpoch)
	h.Write(n[:])
	binary.BigEndian.PutUint64(n[:], round)
	h.Write(n[:])
	h.Write(prevBlockHash)
	var out [MessageLen]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}

// HashToG1 maps a 32-byte message to G1 the way x/bls threshold signing does:
// the 32 bytes as a big-endian Fp element, single-field SWU map + isogeny
// (EIP-2537 MAP_FP_TO_G1), then cofactor clearing.
func HashToG1(msg []byte) (bls12381.G1Affine, error) {
	var out bls12381.G1Affine
	if len(msg) != MessageLen {
		return out, fmt.Errorf("message hash must be %d bytes, got %d", MessageLen, len(msg))
	}
	var be [48]byte
	copy(be[48-MessageLen:], msg)
	var u fp.Element
	u.SetBytes(be[:])
	p := bls12381.MapToCurve1(&u)
	hash_to_curve.G1Isogeny(&p.X, &p.Y)
	out.ClearCofactor(&p)
	return out, nil
}

// HashToG1Blst is HashToG1 as a blst point.
func HashToG1Blst(msg []byte) (*blst.P1Affine, error) {
	p, err := HashToG1(msg)
	if err != nil {
		return nil, err
	}
	b := p.Bytes()
	out := new(blst.P1Affine).Uncompress(b[:])
	if out == nil {
		return nil, errors.New("blsutil: message point does not decode")
	}
	return out, nil
}

// SignPartials signs msg with each scalar (little-endian fr, blst order) and
// returns the 48-byte compressed signatures concatenated, in scalar order.
func SignPartials(msg []byte, scalarsLE [][ScalarLen]byte) ([]byte, error) {
	m, err := HashToG1Blst(msg)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, SignatureLen*len(scalarsLE))
	for i := range scalarsLE {
		sig := new(blst.P1)
		sig.FromAffine(m)
		sig.MultAssign(scalarsLE[i][:], 255)
		out = append(out, sig.ToAffine().Compress()...)
	}
	return out, nil
}

// VerifyPartial checks e(sig, G2) == e(H(msg), slotPublicKey) for one 48-byte
// compressed signature and a compressed G2 slot public key. The signature and
// key are subgroup-checked and must not be the identity.
func VerifyPartial(msg, sig, slotPublicKey []byte) bool {
	if len(sig) != SignatureLen {
		return false
	}
	m, err := HashToG1Blst(msg)
	if err != nil {
		return false
	}
	s := new(blst.P1Affine).Uncompress(sig)
	if s == nil || !s.SigValidate(true) {
		return false
	}
	pk := new(blst.P2Affine).Uncompress(slotPublicKey)
	if pk == nil || !pk.KeyValidate() {
		return false
	}
	negPK := new(blst.P2).Sub(pk).ToAffine()
	g2 := blst.P2Generator().ToAffine()
	ml := blst.Fp12MillerLoopN([]blst.P2Affine{*g2, *negPK}, []blst.P1Affine{*s, *m})
	ml.FinalExp()
	one := blst.Fp12One()
	return ml.Equals(&one)
}
