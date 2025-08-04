package admin

import (
	"decentralized-api/logging"
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
)

func (s *Server) postUnitOfComputePriceProposal(ctx echo.Context) error {
	var body UnitOfComputePriceProposalDto
	if err := ctx.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	price, err := getNanoCoinPrice(&body)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	msg := &inference.MsgSubmitUnitOfComputePriceProposal{
		Price: price,
	}

	if err := s.recorder.SubmitUnitOfComputePriceProposal(msg); err != nil {
		logging.Error("Failed to send a transaction: MsgSubmitUnitOfComputePriceProposal", types.Pricing, "error", err)
		return err
	}
	return ctx.NoContent(http.StatusOK)
}

func (s *Server) getUnitOfComputePriceProposal(ctx echo.Context) error {
	queryClient := s.recorder.NewInferenceQueryClient()
	queryRequest := &types.QueryGetUnitOfComputePriceProposalRequest{
		Participant: s.recorder.GetAccountAddress(),
	}

	queryResponse, err := queryClient.GetUnitOfComputePriceProposal(s.recorder.GetContext(), queryRequest)
	if err != nil {
		logging.Error("Failed to query unit of compute price proposal", types.Pricing, "error", err)
		return err
	}
	return ctx.JSON(http.StatusOK, queryResponse)
}

func getNanoCoinPrice(proposal *UnitOfComputePriceProposalDto) (uint64, error) {
	switch proposal.Denom {
	case types.NanoCoin:
		return proposal.Price, nil
	case types.MicroCoin:
		return proposal.Price * 1_000, nil
	case types.MilliCoin:
		return proposal.Price * 1_000_000, nil
	case types.NativeCoin:
		return proposal.Price * 1_000_000_000, nil
	default:
		return 0, fmt.Errorf("invalid denom: %s", proposal.Denom)
	}
}
