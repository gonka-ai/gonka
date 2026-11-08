package types

import (
	"bytes"
	"fmt"

	"google.golang.org/protobuf/proto"
)

// DiffWire is one diff with its transactions still encoded. Txs is a
// DiffContent encoding: the stored txs_proto has no nonce field, the wire
// form has one. DecodeDiffWire ignores the nonce inside Txs and uses Nonce.
type DiffWire struct {
	Nonce         uint64
	Txs           []byte
	UserSig       []byte
	PostStateRoot []byte
}

// DecodeDiffWire unmarshals Txs and returns the diff identified by w.Nonce.
// proto.Unmarshal copies the transaction bytes. The returned signature and
// root are copies too, so the caller can drop w.
func DecodeDiffWire(w DiffWire) (Diff, error) {
	var content DiffContent
	if len(w.Txs) > 0 {
		if err := proto.Unmarshal(w.Txs, &content); err != nil {
			return Diff{}, fmt.Errorf("unmarshal diff content: %w", err)
		}
	}
	return Diff{
		Nonce:         w.Nonce,
		Txs:           content.Txs,
		UserSig:       bytes.Clone(w.UserSig),
		PostStateRoot: bytes.Clone(w.PostStateRoot),
	}, nil
}
