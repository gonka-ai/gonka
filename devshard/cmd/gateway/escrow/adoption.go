package escrow

import (
	"context"
	"fmt"
	"strconv"

	"devshard/cmd/gateway/store"
)

const adoptionWalkLimit = 128

func (m *Manager) landedEscrow(ctx context.Context, c store.Commitment) (string, bool, error) {
	newest, anchored, err := m.newestEscrowOnChain(ctx)
	if err != nil || !anchored {
		return "", false, err
	}
	signer, err := m.signer.SignerFor(c.PrivateKeyEnv)
	if err != nil {
		return "", false, nil //nolint:nilerr // without its key the gateway could not settle the escrow either
	}
	for offset := uint64(1); offset <= adoptionWalkLimit; offset++ {
		escrowID := strconv.FormatUint(newest+offset, 10)
		info, found, err := m.tx.GetEscrow(ctx, escrowID)
		if err != nil {
			return "", false, fmt.Errorf("reading escrow %s for commitment %s: %w", escrowID, c.TxHash, err)
		}
		if !found {
			return "", false, nil
		}
		if info.Settled || info.Creator != signer.Address() || info.ModelID != c.Model {
			continue
		}
		registered, err := m.escrowRegistered(ctx, escrowID)
		if err != nil {
			return "", false, err
		}
		if !registered {
			return escrowID, true, nil
		}
	}
	return "", false, nil
}

func (m *Manager) newestEscrowOnChain(ctx context.Context) (uint64, bool, error) {
	devshards, err := m.store.ListDevshards(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("listing devshards: %w", err)
	}
	var newest uint64
	anchored := false
	for _, devshard := range devshards {
		escrowID, err := strconv.ParseUint(devshard.EscrowID, 10, 64)
		if err != nil || devshard.GoneFromChain {
			continue
		}
		if !anchored || escrowID > newest {
			newest, anchored = escrowID, true
		}
	}
	return newest, anchored, nil
}
