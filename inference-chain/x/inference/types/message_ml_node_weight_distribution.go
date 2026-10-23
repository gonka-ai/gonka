package types

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

const (
	// MaxMLNodeWeightNodeIDBytes is the structural cap on one node_id.
	// It applies to zero-weight rows as well as positive rows.
	MaxMLNodeWeightNodeIDBytes = 128
	// MaxMLNodeWeightEntriesPerMessage bounds model entries before any map
	// allocation or state lookup.
	MaxMLNodeWeightEntriesPerMessage = 32
	// MaxMLNodeWeightRawRowsPerEntry bounds one model's supplied rows,
	// including rounded zeros an older DAPI still sends. It is a message-size
	// bound, not a hardware-inventory cap and not a governance parameter.
	MaxMLNodeWeightRawRowsPerEntry = 65536
	// MaxMLNodeWeightRawRowsPerMessage bounds the rows in one message across
	// every model entry.
	MaxMLNodeWeightRawRowsPerMessage = 8 * MaxMLNodeWeightRawRowsPerEntry
)

// ValidateRawBounds is the fixed, stateless size check. Call it before
// permission, window, store lookups, and before allocating duplicate maps.
func (msg *MsgMLNodeWeightDistribution) ValidateRawBounds() error {
	if msg == nil {
		return sdkerrors.ErrInvalidRequest.Wrap("nil weight distribution")
	}
	if len(msg.Entries) > MaxMLNodeWeightEntriesPerMessage {
		return sdkerrors.ErrInvalidRequest.Wrapf(
			"too many distribution model entries: %d > %d",
			len(msg.Entries), MaxMLNodeWeightEntriesPerMessage,
		)
	}
	total := 0
	for _, entry := range msg.Entries {
		n := 0
		if entry != nil {
			n = len(entry.Weights)
		}
		if n > MaxMLNodeWeightRawRowsPerEntry {
			return sdkerrors.ErrInvalidRequest.Wrapf(
				"too many weight rows in one model entry: %d > %d",
				n, MaxMLNodeWeightRawRowsPerEntry,
			)
		}
		if total > MaxMLNodeWeightRawRowsPerMessage-n {
			return sdkerrors.ErrInvalidRequest.Wrapf(
				"too many weight rows in one message: %d > %d",
				total+n, MaxMLNodeWeightRawRowsPerMessage,
			)
		}
		total += n
	}
	return nil
}

// ValidateBasic checks raw bounds and structural shape. It does not read
// state and does not mutate the message.
func (msg *MsgMLNodeWeightDistribution) ValidateBasic() error {
	if err := msg.ValidateRawBounds(); err != nil {
		return err
	}
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return sdkerrors.ErrInvalidAddress.Wrapf("invalid creator address: %s", err)
	}
	_, err := CanonicalMLNodeWeightEntries(msg.Entries)
	return err
}

// CanonicalMLNodeWeightEntries checks structural shape and returns a new
// slice of positive-weight rows. Zero-weight rows are omitted. The input is
// not modified. An empty canonical slice is rejected: a message of only
// zeros is not a distribution.
func CanonicalMLNodeWeightEntries(entries []*MLNodeDistributionEntry) ([]*MLNodeDistributionEntry, error) {
	if len(entries) == 0 {
		return nil, sdkerrors.ErrInvalidRequest.Wrap("entries must not be empty")
	}
	seenModels := make(map[string]struct{}, len(entries))
	out := make([]*MLNodeDistributionEntry, 0, len(entries))
	for _, entry := range entries {
		if entry == nil {
			return nil, sdkerrors.ErrInvalidRequest.Wrap("nil distribution entry")
		}
		if entry.ModelId == "" {
			return nil, sdkerrors.ErrInvalidRequest.Wrap("model_id must not be empty")
		}
		if _, dup := seenModels[entry.ModelId]; dup {
			return nil, sdkerrors.ErrInvalidRequest.Wrapf("duplicate model_id %q", entry.ModelId)
		}
		seenModels[entry.ModelId] = struct{}{}

		seenNodes := make(map[string]struct{}, len(entry.Weights))
		positive := make([]*MLNodeWeight, 0, len(entry.Weights))
		for _, weight := range entry.Weights {
			if weight == nil {
				return nil, sdkerrors.ErrInvalidRequest.Wrap("nil weight row")
			}
			if weight.NodeId == "" {
				return nil, sdkerrors.ErrInvalidRequest.Wrap("node_id must not be empty")
			}
			if len(weight.NodeId) > MaxMLNodeWeightNodeIDBytes {
				return nil, sdkerrors.ErrInvalidRequest.Wrapf(
					"node_id longer than %d bytes", MaxMLNodeWeightNodeIDBytes,
				)
			}
			if _, dup := seenNodes[weight.NodeId]; dup {
				return nil, sdkerrors.ErrInvalidRequest.Wrapf("duplicate node_id %q", weight.NodeId)
			}
			seenNodes[weight.NodeId] = struct{}{}
			if weight.Weight == 0 {
				continue
			}
			positive = append(positive, &MLNodeWeight{
				NodeId: weight.NodeId,
				Weight: weight.Weight,
			})
		}
		if len(positive) == 0 {
			return nil, sdkerrors.ErrInvalidRequest.Wrapf(
				"model %q has no positive weight rows", entry.ModelId,
			)
		}
		out = append(out, &MLNodeDistributionEntry{
			ModelId: entry.ModelId,
			Weights: positive,
		})
	}
	return out, nil
}
