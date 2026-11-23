package transport

import (
	"errors"
	"fmt"
	"slices"

	"google.golang.org/protobuf/encoding/protowire"

	"devshard/host"
	"devshard/transport/rpcpb"
	"devshard/types"
)

// MaxDecodedDiffs bounds the diffs in one peer request: a challenge page
// holds at most host.ChallengePageMaxNonces.
const MaxDecodedDiffs = host.ChallengePageMaxNonces

// CatchUpMaxDiffs bounds owner chat and the verify RPCs: one catch-up chunk
// plus the target diff.
const CatchUpMaxDiffs = host.CatchUpChunkSize + 1

// DiffLimits bound one request that carries diffs.
type DiffLimits struct {
	Diffs int
	types.DecodeLimits
}

var (
	// PageLimits fit a full challenge page (ChallengeReceipt).
	PageLimits = DiffLimits{Diffs: MaxDecodedDiffs, DecodeLimits: types.PageDecodeLimits}
	// CatchUpLimits scale the page budgets down to CatchUpMaxDiffs.
	CatchUpLimits = DiffLimits{
		Diffs:        CatchUpMaxDiffs,
		DecodeLimits: types.PageDecodeLimits.Scaled(CatchUpMaxDiffs, MaxDecodedDiffs),
	}
)

func (l DiffLimits) tooManyDiffs() error {
	return fmt.Errorf("%w: more than %d diffs", types.ErrDecodeLimit, l.Diffs)
}

// countDiffsJSON checks decoded JSON diffs against lim before their txs are
// unmarshalled.
func countDiffsJSON(djs []DiffJSON, lim DiffLimits) error {
	if len(djs) > lim.Diffs {
		return lim.tooManyDiffs()
	}
	count := types.TxCount{Limits: lim.DecodeLimits}
	for i, dj := range djs {
		err := count.AddDiffContent(dj.Txs)
		if errors.Is(err, types.ErrDecodeLimit) {
			return fmt.Errorf("%w across diffs", err)
		}
		if err != nil {
			return fmt.Errorf("decode diff %d: %w", i, err)
		}
	}
	return nil
}

var diffTxsField = (&rpcpb.Diff{}).ProtoReflect().Descriptor().Fields().ByName("txs").Number()

// CheckDiffsPayload counts the Diff messages in field diffsField of an encoded
// request, and the txs and tx entries inside them, against lim before
// proto.Unmarshal allocates them. Txs in txFields draw on the same budget.
func CheckDiffsPayload(b []byte, lim DiffLimits, diffsField protowire.Number, txFields ...protowire.Number) error {
	diffs := 0
	count := types.TxCount{Limits: lim.DecodeLimits}
	err := walkFields(b, func(num protowire.Number, v []byte) error {
		if slices.Contains(txFields, num) {
			return limitOnly(count.AddTx(v))
		}
		if num != diffsField {
			return nil
		}
		if diffs++; diffs > lim.Diffs {
			return lim.tooManyDiffs()
		}
		return walkFields(v, func(num protowire.Number, v []byte) error {
			if num != diffTxsField {
				return nil
			}
			return limitOnly(count.AddDiffContent(v))
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

// CheckTxsPayload counts the txs in repeated field txsField and the entries
// inside them.
func CheckTxsPayload(b []byte, txsField protowire.Number) error {
	var count types.TxCount
	return limitOnly(walkFields(b, func(num protowire.Number, v []byte) error {
		if num != txsField {
			return nil
		}
		return limitOnly(count.AddTx(v))
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
