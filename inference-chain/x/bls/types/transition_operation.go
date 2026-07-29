package types

import "golang.org/x/crypto/sha3"

// TransitionOperationName is the preimage of the group-key rotation domain tag.
const TransitionOperationName = "TRANSITION_OPERATION"

// transitionOperationTag is keccak256("TRANSITION_OPERATION"), the 32-byte domain
// tag inserted at offset 40 of the rotation validation message:
//
//	keccak256( previous_epoch_id[8] || gonka_chain_id[32] || TRANSITION_OPERATION[32] || new_group_key[256] )
//
var transitionOperationTag = func() [32]byte {
	h := sha3.NewLegacyKeccak256()
	h.Write([]byte(TransitionOperationName))
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}()

// TransitionOperationTag returns a fresh copy of the 32-byte domain-separation tag
// keccak256("TRANSITION_OPERATION") for the group-key rotation message.
func TransitionOperationTag() []byte {
	out := make([]byte, 32)
	copy(out, transitionOperationTag[:])
	return out
}
