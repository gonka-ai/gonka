package hosts

import (
	"testing"

	"trainshard/internal/contract"
	"trainshard/internal/domain/shared/vo"
)

func TestAnIdentityWithAnUnreadableSignatureLeavesTheOthersOfTheHost(t *testing.T) {
	// arrange
	items := []contract.MeshIdentity{
		{NodeID: "node-1", Address: "10.0.0.1", PublicKey: "key-1", Signature: "not hex"},
		{NodeID: "node-2", Address: "10.0.0.2", PublicKey: "key-2", Signature: "0a0b"},
	}

	// act
	identities := toIdentities("gonka1host", items)

	// assert
	if len(identities) != 1 || identities[0].Member.Node != (vo.NodeRef{Participant: "gonka1host", NodeID: "node-2"}) {
		t.Fatalf("got %+v, want only the identity whose signature can be read", identities)
	}
	if string(identities[0].Signature) != "\x0a\x0b" {
		t.Fatalf("got signature %x, want 0a0b", identities[0].Signature)
	}
}
