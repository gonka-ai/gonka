package types

import (
	"bytes"
	"errors"
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// MaxDecodedTxs bounds the transactions decoded from one peer message, summed
// over all its diffs or mempool entries. An empty tx is 2 bytes on the wire and
// ~80 B decoded, so the byte cap alone admits ~40x its size in heap.
const MaxDecodedTxs = 1 << 16

var ErrDecodeLimit = errors.New("decode limit")

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
		if _, err := CountDiffTxs(w.Txs, MaxDecodedTxs); err != nil {
			return Diff{}, err
		}
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

// CountDiffTxs counts the txs in a DiffContent encoding without decoding them.
// It fails once the count exceeds limit.
func CountDiffTxs(b []byte, limit int) (int, error) {
	n := 0
	for len(b) > 0 {
		num, typ, l := protowire.ConsumeTag(b)
		if l < 0 {
			return n, fmt.Errorf("unmarshal diff content: %w", protowire.ParseError(l))
		}
		b = b[l:]
		if num == 2 {
			if n++; n > limit {
				return n, fmt.Errorf("%w: more than %d in diff content", ErrDecodeLimit, limit)
			}
		}
		l = protowire.ConsumeFieldValue(num, typ, b)
		if l < 0 {
			return n, fmt.Errorf("unmarshal diff content: %w", protowire.ParseError(l))
		}
		b = b[l:]
	}
	return n, nil
}
