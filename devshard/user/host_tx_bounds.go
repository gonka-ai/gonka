package user

import (
	"fmt"
	"slices"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"devshard/host"
	"devshard/types"
)

var (
	maxHostTxBytes      = 2 << 10
	maxDiffTxBytes      = 3 << 20
	maxCatchUpWireBytes = 6 << 20
)

const diffWireOverheadBytes = 256

func canonicalHostTx(tx *types.DevshardTx) *types.DevshardTx {
	if tx == nil || !hasUnknownFields(tx.ProtoReflect()) {
		return tx
	}
	clean := proto.Clone(tx).(*types.DevshardTx)
	clearUnknownFields(clean.ProtoReflect())
	return clean
}

func hasUnknownFields(m protoreflect.Message) bool {
	if len(m.GetUnknown()) > 0 {
		return true
	}
	found := false
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsList() && fd.Message() != nil:
			list := v.List()
			for i := 0; i < list.Len(); i++ {
				if hasUnknownFields(list.Get(i).Message()) {
					found = true
					return false
				}
			}
		case fd.IsMap() && fd.MapValue().Message() != nil:
			v.Map().Range(func(_ protoreflect.MapKey, mv protoreflect.Value) bool {
				if hasUnknownFields(mv.Message()) {
					found = true
				}
				return !found
			})
		case !fd.IsList() && !fd.IsMap() && fd.Message() != nil:
			found = hasUnknownFields(v.Message())
		}
		return !found
	})
	return found
}

func clearUnknownFields(m protoreflect.Message) {
	if len(m.GetUnknown()) > 0 {
		m.SetUnknown(nil)
	}
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsList() && fd.Message() != nil:
			list := v.List()
			for i := 0; i < list.Len(); i++ {
				clearUnknownFields(list.Get(i).Message())
			}
		case fd.IsMap() && fd.MapValue().Message() != nil:
			v.Map().Range(func(_ protoreflect.MapKey, mv protoreflect.Value) bool {
				clearUnknownFields(mv.Message())
				return true
			})
		case !fd.IsList() && !fd.IsMap() && fd.Message() != nil:
			clearUnknownFields(v.Message())
		}
		return true
	})
}

func base64Len(n int) int {
	return 4 * ((n + 2) / 3)
}

func diffWireBytes(d types.Diff) int {
	content := &types.DiffContent{Nonce: d.Nonce, Txs: d.Txs}
	return base64Len(proto.Size(content)) + base64Len(len(d.UserSig)) + base64Len(len(d.PostStateRoot)) + diffWireOverheadBytes
}

func wireBudgetPrefix(diffs []types.Diff, budget int) int {
	total := 0
	for i, d := range diffs {
		total += diffWireBytes(d)
		if total > budget && i > 0 {
			return i
		}
	}
	return len(diffs)
}

func admissibleHostMempool(resp *host.HostResponse) *host.HostResponse {
	changed := false
	mempool := make([]*types.DevshardTx, 0, len(resp.Mempool))
	for _, tx := range resp.Mempool {
		clean := canonicalHostTx(tx)
		if clean != tx {
			changed = true
		}
		if clean != nil && proto.Size(clean) > maxHostTxBytes {
			changed = true
			continue
		}
		mempool = append(mempool, clean)
	}
	if !changed {
		return resp
	}
	out := *resp
	out.Mempool = mempool
	return &out
}

func (s *Session) boundPendingToDiffBudgetLocked(nonce uint64, pending, extraTxs []*types.DevshardTx, includePinned []uint64) (kept, held []*types.DevshardTx, err error) {
	budget := maxDiffTxBytes
	for _, tx := range extraTxs {
		budget -= proto.Size(tx)
	}
	include := make(map[uint64]struct{}, len(includePinned))
	for _, id := range includePinned {
		include[id] = struct{}{}
	}
	mandatory := make(map[*types.DevshardTx]struct{})
	total := 0
	for _, tx := range pending {
		total += proto.Size(tx)
		if id, ok := errorMissPendingID(tx); ok {
			if _, pinned := include[id]; pinned {
				mandatory[tx] = struct{}{}
			}
		}
	}
	if total <= budget || s.sm == nil {
		return pending, nil, nil
	}
	vd, err := s.sm.PreviewLocalBestEffort(nonce, slices.Concat(pending, extraTxs))
	if err != nil {
		return nil, nil, fmt.Errorf("preview diff tx budget: %w", err)
	}
	applied := make(map[*types.DevshardTx]struct{}, len(vd.Applied))
	for _, tx := range vd.Applied {
		applied[tx] = struct{}{}
	}
	kept, held = boundAppliedToBudget(pending, applied, mandatory, budget)
	return kept, held, nil
}

func boundAppliedToBudget(pending []*types.DevshardTx, applied, mandatory map[*types.DevshardTx]struct{}, budget int) (kept, held []*types.DevshardTx) {
	for tx := range mandatory {
		budget -= proto.Size(tx)
	}
	cut := false
	for _, tx := range pending {
		if _, ok := mandatory[tx]; ok {
			kept = append(kept, tx)
			continue
		}
		if _, ok := applied[tx]; !ok {
			kept = append(kept, tx)
			continue
		}
		size := proto.Size(tx)
		if cut || size > budget {
			cut = true
			held = append(held, tx)
			continue
		}
		budget -= size
		kept = append(kept, tx)
	}
	return kept, held
}
