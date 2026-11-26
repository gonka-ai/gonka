package timeoutclaim

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"devshard/types"
)

const clientFaultPayload = `{"events":["data: {\"error\":{\"code\":400,\"message\":\"logit_bias key out of range\",\"type\":\"BadRequestError\"}}"]}`

type servedSiblings map[uint64]bool

func (s servedSiblings) ServedSibling(_, sibling uint64) bool { return s[sibling] }

func finishTxBilling(t *testing.T, nonce, inputTokens, outputTokens uint64) []byte {
	t.Helper()
	finishTx, err := proto.Marshal(&types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{
		InferenceId: nonce, InputTokens: inputTokens, OutputTokens: outputTokens,
	}}})
	if err != nil {
		t.Fatalf("proto.Marshal(): %v", err)
	}
	return finishTx
}

// Test flow:
//  1. Ask ClaimSibling about an unbilled client fault with no sibling that served the prompt, with only the claimed nonce itself served, and with a served sibling.
//  2. Ask it about the same error under a Finish that billed tokens, and under no decodable Finish.
//  3. Assert the client fault is claimable only beside a served sibling other than itself, which it names, and every other claim is claimable naming NoSibling.
func TestClaimSiblingHoldsAClientFaultClaimToAServedSibling(t *testing.T) {
	const nonce = uint64(7)
	unbilled := finishTxBilling(t, nonce, 0, 0)
	billed := finishTxBilling(t, nonce, 12, 3)
	siblings := []uint64{nonce, 8, 9}

	cases := []struct {
		name          string
		finishTx      []byte
		served        servedSiblings
		wantSibling   uint64
		wantClaimable bool
	}{
		{name: "client fault, no sibling served", finishTx: unbilled, served: servedSiblings{}, wantSibling: NoSibling, wantClaimable: false},
		{name: "client fault, only itself served", finishTx: unbilled, served: servedSiblings{nonce: true}, wantSibling: NoSibling, wantClaimable: false},
		{name: "client fault, a sibling served", finishTx: unbilled, served: servedSiblings{9: true}, wantSibling: 9, wantClaimable: true},
		{name: "billed finish", finishTx: billed, served: servedSiblings{}, wantSibling: NoSibling, wantClaimable: true},
		{name: "undecodable finish", finishTx: []byte("not a finish"), served: servedSiblings{}, wantSibling: NoSibling, wantClaimable: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			sibling, claimable := ClaimSibling(nonce, testCase.finishTx, []byte(clientFaultPayload), siblings, testCase.served)
			if sibling != testCase.wantSibling || claimable != testCase.wantClaimable {
				t.Fatalf("ClaimSibling() = (%d, %t), want (%d, %t)", sibling, claimable, testCase.wantSibling, testCase.wantClaimable)
			}
		})
	}
}
