// Package blsutil holds the pure BLS primitives of the block beacon
// (proposals/block-bls-beacon/block-bls-beacon-vote-extensions-plan.md): the
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
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
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

// AggregatePartials combines per-slot partial signatures into the group
// signature with Lagrange coefficients at 0 (slot s is the share at x = s+1).
// slots must be distinct; sigs[i] is the 48-byte signature of slots[i]. Any
// t+1 valid partials of the same message give the same result.
func AggregatePartials(slots []uint32, sigs [][]byte) ([]byte, error) {
	if len(slots) == 0 || len(slots) != len(sigs) {
		return nil, fmt.Errorf("blsutil: %d slots for %d signatures", len(slots), len(sigs))
	}
	xs := make([]fr.Element, len(slots))
	seen := make(map[uint32]bool, len(slots))
	points := make([]*blst.P1Affine, len(slots))
	for i, s := range slots {
		if seen[s] {
			return nil, fmt.Errorf("blsutil: duplicate slot %d", s)
		}
		seen[s] = true
		xs[i].SetUint64(uint64(s) + 1)
		if len(sigs[i]) != SignatureLen {
			return nil, fmt.Errorf("blsutil: signature for slot %d is %d bytes", s, len(sigs[i]))
		}
		p := new(blst.P1Affine).Uncompress(sigs[i])
		if p == nil || !p.SigValidate(true) {
			return nil, fmt.Errorf("blsutil: invalid signature for slot %d", s)
		}
		points[i] = p
	}
	scalars := make([]byte, 0, ScalarLen*len(slots))
	for i := range xs {
		var num, den fr.Element
		num.SetOne()
		den.SetOne()
		for j := range xs {
			if j == i {
				continue
			}
			var neg, diff fr.Element
			neg.Neg(&xs[j])
			num.Mul(&num, &neg)
			diff.Sub(&xs[i], &xs[j])
			den.Mul(&den, &diff)
		}
		var lam fr.Element
		den.Inverse(&den)
		lam.Mul(&num, &den)
		be := lam.Bytes()
		for k := 0; k < ScalarLen/2; k++ {
			be[k], be[ScalarLen-1-k] = be[ScalarLen-1-k], be[k]
		}
		scalars = append(scalars, be[:]...)
	}
	return blst.P1AffinesMult(points, scalars, 255).ToAffine().Compress(), nil
}

// VerifySignature checks a group (or slot) signature over msg against a
// compressed G2 public key: e(sig, G2) == e(H(msg), pk).
func VerifySignature(msg, sig, publicKey []byte) bool { return VerifyPartial(msg, sig, publicKey) }
