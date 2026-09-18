package transport

import (
	"encoding/base64"

	"google.golang.org/protobuf/proto"

	"devshard/types"
)

const (
	diffJSONOverheadBytes         = 96
	promptJSONOverheadBytes       = 256
	jsonStringExpansion           = 6
	bytesJSONOverheadBytes        = 64
	inferenceEnvelopeReserveBytes = 1 << 20
)

const CatchUpBudgetBytes = int(DefaultMaxBodySize) - inferenceEnvelopeReserveBytes

func EncodedDiffSize(diff types.Diff) int {
	content := &types.DiffContent{Nonce: diff.Nonce, Txs: diff.Txs}
	return diffJSONOverheadBytes +
		base64.StdEncoding.EncodedLen(proto.Size(content)) +
		base64.StdEncoding.EncodedLen(len(diff.UserSig)) +
		base64.StdEncoding.EncodedLen(len(diff.PostStateRoot))
}

func EncodedPromptSize(prompt []byte, model string) int {
	return promptJSONOverheadBytes + base64.StdEncoding.EncodedLen(len(prompt)) + jsonStringExpansion*len(model)
}

func EncodedBytesSize(raw []byte) int {
	return bytesJSONOverheadBytes + base64.StdEncoding.EncodedLen(len(raw))
}
