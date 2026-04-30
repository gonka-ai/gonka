package inference

import (
	"testing"

	blstypes "github.com/productscience/inference/x/bls/types"
)

func TestInferenceOperationKeyPermsIncludesRespondDealerComplaints(t *testing.T) {
	found := false
	for _, msg := range InferenceOperationKeyPerms {
		if _, ok := msg.(*blstypes.MsgRespondDealerComplaints); ok {
			found = true
			break
		}
	}

	if !found {
		t.Fatalf("InferenceOperationKeyPerms must include MsgRespondDealerComplaints")
	}
}
