package inference

import (
	"testing"

	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/types"
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

func TestInferenceOperationKeyPermsIncludesDeclarePoCIntent(t *testing.T) {
	found := false
	for _, msg := range InferenceOperationKeyPerms {
		if _, ok := msg.(*types.MsgDeclarePoCIntent); ok {
			found = true
			break
		}
	}

	if !found {
		t.Fatalf("InferenceOperationKeyPerms must include MsgDeclarePoCIntent")
	}
}
