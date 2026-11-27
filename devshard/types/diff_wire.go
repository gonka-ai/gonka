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

// MaxDecodedTxEntries bounds the repeated entries inside those txs (timeout
// and error-miss votes, heartbeat sync_vector), summed the same way. A full
// challenge page of heartbeat diffs honestly carries 8192 x MaxGroupSize.
const MaxDecodedTxEntries = 1 << 20

var ErrDecodeLimit = errors.New("decode limit")

// DecodeLimits are the tx and entry budgets of one peer message. The zero
// value means PageDecodeLimits.
type DecodeLimits struct {
	Txs     int
	Entries int
}

// PageDecodeLimits fit a full challenge page.
var PageDecodeLimits = DecodeLimits{Txs: MaxDecodedTxs, Entries: MaxDecodedTxEntries}

// Scaled returns l for a message of diffs diffs instead of pageDiffs.
func (l DecodeLimits) Scaled(diffs, pageDiffs int) DecodeLimits {
	scale := func(n int) int { return (n*diffs + pageDiffs - 1) / pageDiffs }
	return DecodeLimits{Txs: scale(l.Txs), Entries: scale(l.Entries)}
}

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
		var c TxCount
		if err := c.AddDiffContent(w.Txs); err != nil {
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

// TxCount counts the txs of one peer message, and the repeated entries inside
// them, from their encoding, without decoding them.
type TxCount struct {
	Txs     int
	Entries int
	Limits  DecodeLimits
}

func (c *TxCount) limits() DecodeLimits {
	if c.Limits == (DecodeLimits{}) {
		return PageDecodeLimits
	}
	return c.Limits
}

// txEntryFields maps a DevshardTx field to the repeated field of the message
// it wraps; these are the only repeated fields under DevshardTx. Numbers, not
// descriptors: package vars initialize before the generated file descriptors.
var txEntryFields = map[protowire.Number]protowire.Number{
	4:  3, // timeout_inference: MsgTimeoutInference.votes
	14: 2, // error_miss: MsgErrorMiss.votes
	10: 6, // heartbeat: MsgHeartbeat.sync_vector
}

// AddDiffContent counts a DiffContent encoding. It fails once either cap is
// exceeded or the encoding does not parse.
func (c *TxCount) AddDiffContent(b []byte) error {
	return eachField(b, func(num protowire.Number, v []byte) error {
		if num != 2 {
			return nil
		}
		return c.AddTx(v)
	})
}

// AddTx counts one DevshardTx encoding. A single votes or sync_vector list
// holds one entry per slot, so it is refused above MaxGroupSize.
func (c *TxCount) AddTx(b []byte) error {
	lim := c.limits()
	if c.Txs++; c.Txs > lim.Txs {
		return fmt.Errorf("%w: more than %d txs", ErrDecodeLimit, lim.Txs)
	}
	// A message field repeated on the wire is merged, its lists appended.
	perList := map[protowire.Number]int{}
	return eachField(b, func(num protowire.Number, v []byte) error {
		inner, ok := txEntryFields[num]
		if !ok {
			return nil
		}
		return eachField(v, func(n protowire.Number, _ []byte) error {
			if n != inner {
				return nil
			}
			if perList[num]++; perList[num] > MaxGroupSize {
				return fmt.Errorf("%w: more than %d entries in one vote or sync_vector list", ErrDecodeLimit, MaxGroupSize)
			}
			if c.Entries++; c.Entries > lim.Entries {
				return fmt.Errorf("%w: more than %d vote or sync_vector entries in txs", ErrDecodeLimit, lim.Entries)
			}
			return nil
		})
	})
}

// eachField calls fn for every field of b; v is the value of a
// length-delimited field and nil for other wire types.
func eachField(b []byte, fn func(num protowire.Number, v []byte) error) error {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return fmt.Errorf("unmarshal diff content: %w", protowire.ParseError(n))
		}
		b = b[n:]
		var v []byte
		if typ == protowire.BytesType {
			v, n = protowire.ConsumeBytes(b)
		} else {
			n = protowire.ConsumeFieldValue(num, typ, b)
		}
		if n < 0 {
			return fmt.Errorf("unmarshal diff content: %w", protowire.ParseError(n))
		}
		b = b[n:]
		if err := fn(num, v); err != nil {
			return err
		}
	}
	return nil
}
