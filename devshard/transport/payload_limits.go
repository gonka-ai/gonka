package transport

import (
	"errors"
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"

	"devshard/host"
	"devshard/transport/rpcpb"
	"devshard/types"
)

// MaxDecodedDiffs bounds the diffs in one peer request: a challenge page
// holds at most host.ChallengePageMaxNonces.
const MaxDecodedDiffs = host.ChallengePageMaxNonces

var diffTxsField = (&rpcpb.Diff{}).ProtoReflect().Descriptor().Fields().ByName("txs").Number()

// CheckDiffsPayload counts the Diff messages in field diffsField of an encoded
// request, and the txs inside them, before proto.Unmarshal allocates them.
func CheckDiffsPayload(b []byte, diffsField protowire.Number) error {
	diffs, txs := 0, 0
	err := walkFields(b, func(num protowire.Number, v []byte) error {
		if num != diffsField {
			return nil
		}
		if diffs++; diffs > MaxDecodedDiffs {
			return fmt.Errorf("%w: more than %d diffs", types.ErrDecodeLimit, MaxDecodedDiffs)
		}
		return walkFields(v, func(num protowire.Number, v []byte) error {
			if num != diffTxsField {
				return nil
			}
			n, err := types.CountDiffTxs(v, types.MaxDecodedTxs-txs)
			txs += n
			return limitOnly(err)
		})
	})
	if err = limitOnly(err); err != nil {
		return fmt.Errorf("diffs: %w", err)
	}
	return nil
}

// limitOnly drops parse errors: a malformed payload or diff is left to the
// decoder, which a bound host does not run on challenge diffs.
func limitOnly(err error) error {
	if errors.Is(err, types.ErrDecodeLimit) {
		return err
	}
	return nil
}

// CheckTxsPayload counts the entries of repeated field txsField.
func CheckTxsPayload(b []byte, txsField protowire.Number) error {
	n := 0
	return limitOnly(walkFields(b, func(num protowire.Number, _ []byte) error {
		if num == txsField {
			if n++; n > types.MaxDecodedTxs {
				return fmt.Errorf("%w: more than %d", types.ErrDecodeLimit, types.MaxDecodedTxs)
			}
		}
		return nil
	}))
}

// walkFields calls fn for every length-delimited field of b; other wire
// types are skipped.
func walkFields(b []byte, fn func(protowire.Number, []byte) error) error {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]
		if typ == protowire.BytesType {
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return protowire.ParseError(m)
			}
			if err := fn(num, v); err != nil {
				return err
			}
			b = b[m:]
			continue
		}
		n = protowire.ConsumeFieldValue(num, typ, b)
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]
	}
	return nil
}
