package queryapi

import (
	"net/http"

	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/labstack/echo/v4"
	inferencetypes "github.com/productscience/inference/x/inference/types"

	"common/queryapi/gen"
)

func (h *Handlers) GetParticipants(ctx echo.Context) error {
	var participants []gen.ParticipantDto
	var blockHeight int64
	var nextKey []byte

	qc := h.chain.InferenceQueryClient()
	for {
		resp, err := qc.ParticipantsWithBalances(ctx.Request().Context(),
			&inferencetypes.QueryParticipantsWithBalancesRequest{
				Pagination: &query.PageRequest{Key: nextKey, Limit: 1000},
			},
		)
		if err != nil {
			return grpcErrorToHTTP(err)
		}
		if blockHeight == 0 {
			blockHeight = resp.BlockHeight
		}
		for _, pwb := range resp.Participants {
			balance := balanceNGonka(pwb.Balances)
			participants = append(participants, gen.ParticipantDto{
				Id:          pwb.Participant.Address,
				Url:         pwb.Participant.InferenceUrl,
				Balance:     int(balance),
				VotingPower: int(pwb.Participant.Weight),
			})
		}
		if resp.Pagination == nil || len(resp.Pagination.NextKey) == 0 {
			break
		}
		nextKey = resp.Pagination.NextKey
	}

	return ctx.JSON(http.StatusOK, gen.ParticipantsResponse{
		Participants: participants,
		BlockHeight:  int(blockHeight),
	})
}

func (h *Handlers) GetParticipant(ctx echo.Context, address string) error {
	resp, err := h.chain.InferenceQueryClient().AccountByAddress(
		ctx.Request().Context(),
		&inferencetypes.QueryAccountByAddressRequest{Address: address},
	)
	if err != nil {
		return grpcErrorToHTTP(err)
	}
	return ctx.JSON(http.StatusOK, gen.AccountDto{
		Pubkey:  resp.Pubkey,
		Balance: int(resp.Balance),
		Denom:   resp.Denom,
	})
}
