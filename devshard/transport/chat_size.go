package transport

import (
	"encoding/base64"
	"encoding/json"
	"errors"

	"devshard/host"
	"devshard/types"

	"google.golang.org/protobuf/proto"
)

var ErrChatRequestTooLarge = errors.New("encoded chat request exceeds the host body limit")

// ChatDiffPrefix returns the largest prefix that fits in a default chat
// request. It sizes the actual JSON wire fields, including base64 inflation,
// without allocating copies of the prompt or proof. The prompt travels as
// raw bytes in the protobuf envelope. Reserve room for the optional
// height-sync anchor, protobuf tags and lengths, and the Connect signature.
func ChatDiffPrefix(req host.HostRequest, escrowID string) (int, error) {
	const envelopeOverhead = 1024
	diffs := req.Diffs
	req.Diffs = nil
	wire, err := HostRequestToJSON(req)
	if err != nil {
		return 0, err
	}
	wire, prompt := detachChatPrompt(wire)
	body, err := json.Marshal(wire)
	if err != nil {
		return 0, err
	}
	size := int64(len(body) + len(prompt) + len(escrowID) + envelopeOverhead)
	if size > DefaultMaxBodySize {
		return 0, ErrChatRequestTooLarge
	}
	for i, diff := range diffs {
		// The empty Txs slice contributes the quotes around base64 JSON.
		row, err := json.Marshal(DiffJSON{Nonce: diff.Nonce, Txs: []byte{}, UserSig: diff.UserSig, PostStateRoot: diff.PostStateRoot})
		if err != nil {
			return 0, err
		}
		txs := proto.Size(&types.DiffContent{Nonce: diff.Nonce, Txs: diff.Txs})
		size += int64(len(row) + base64.StdEncoding.EncodedLen(txs))
		if i > 0 {
			size++ // comma between diffs
		}
		if size > DefaultMaxBodySize {
			return i, nil
		}
	}
	return len(diffs), nil
}
