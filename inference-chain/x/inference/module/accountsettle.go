package inference

import (
	"context"
	"github.com/productscience/inference/x/inference/types"
)

func (am AppModule) SettleAccounts(ctx context.Context) error {
	participants, err := am.keeper.ParticipantAll(ctx, &types.QueryAllParticipantRequest{})
	if err != nil {
		am.LogError("Error getting participants", "error", err)
		return err
	}

	for _, p := range participants.Participant {
		if p.CoinBalance == 0 && p.RefundBalance == 0 {
			continue
		}
		err = am.keeper.SettleParticipant(ctx, &p)
		am.keeper.SetParticipant(ctx, p)
		if err != nil {
			return err
		}
	}

	return nil
}
