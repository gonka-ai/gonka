package public

import (
	"context"
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/internal/utils"
	"decentralized-api/logging"
	"fmt"
	"github.com/gonka-ai/gonka-utils/go/contracts"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
	"strconv"
)

func (s *Server) getInferenceParticipantByAddress(c echo.Context) error {
	address := c.Param("address")
	if address == "" {
		return ErrAddressRequired
	}

	logging.Debug("GET inference participant", types.Inferences, "address", address)

	queryClient := s.recorder.NewInferenceQueryClient()
	response, err := queryClient.InferenceParticipant(c.Request().Context(), &types.QueryInferenceParticipantRequest{
		Address: address,
	})
	if err != nil {
		logging.Error("Failed to get inference participant", types.Inferences, "address", address, "error", err)
		return err
	}

	if response == nil {
		logging.Error("Inference participant not found", types.Inferences, "address", address)
		return ErrInferenceParticipantNotFound
	}

	return c.JSON(http.StatusOK, response)
}

func (s *Server) getParticipantsByEpoch(c echo.Context) error {
	epoch, err := s.resolveEpochFromContext(c)
	if err != nil {
		logging.Error("Failed to resolve epoch from context", types.Server, "error", err)
		return err
	}

	resp, err := s.getParticipants(epoch)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, resp)
}

// resolveEpochFromContext extracts the epoch from the context parameters.
// If the epoch is "current", it returns nil
func (s *Server) resolveEpochFromContext(c echo.Context) (uint64, error) {
	epochParam := c.Param("epoch")
	if epochParam == "" {
		return 0, ErrInvalidEpochId
	}

	if epochParam == "current" {
		queryClient := s.recorder.NewInferenceQueryClient()
		currEpoch, err := queryClient.GetCurrentEpoch(*s.recorder.GetContext(), &types.QueryGetCurrentEpochRequest{})
		if err != nil {
			logging.Error("Failed to get current epoch", types.Participants, "error", err)
			return 0, err
		}
		logging.Info("Current epoch resolved.", types.Participants, "epoch", currEpoch.Epoch)
		return currEpoch.Epoch, nil
	} else {
		epochId, err := strconv.ParseUint(epochParam, 10, 64)
		if err != nil {
			return 0, ErrInvalidEpochId
		}
		return epochId, nil
	}
}

func (s *Server) getParticipants(epoch uint64) (*contracts.ActiveParticipantWithProof, error) {
	// FIXME: now we can set active participants even for epoch 0, fix InitGenesis for that
	if epoch == 0 {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "Epoch enumeration starts with 1")
	}

	rpcClient, err := cosmos_client.NewRpcClient(s.configManager.GetChainNodeConfig().Url)
	if err != nil {
		logging.Error("Failed to create rpc client", types.System, "error", err)
		return nil, err
	}

	activeParticipants, err := utils.QueryActiveParticipants(rpcClient, epoch)(context.Background(), fmt.Sprintf("%v", epoch))
	if err != nil {
		logging.Error("Failed to query active participants. Outer", types.Participants, "error", err)
		return nil, err
	}
	return activeParticipants, nil
}

func (s *Server) getAllParticipants(ctx echo.Context) error {
	queryClient := s.recorder.NewInferenceQueryClient()
	r, err := queryClient.ParticipantAll(ctx.Request().Context(), &types.QueryAllParticipantRequest{})
	if err != nil {
		return err
	}

	participants := make([]ParticipantDto, len(r.Participant))
	for i, p := range r.Participant {
		balances, err := s.recorder.BankBalances(ctx.Request().Context(), p.Address)
		pBalance := int64(0)
		if err == nil {
			for _, balance := range balances {
				// TODO: surely there is a place to get denom from
				if balance.Denom == "nicoin" {
					pBalance = balance.Amount.Int64()
				}
			}
			if pBalance == 0 {
				logging.Debug("Participant has no balance", types.Participants, "address", p.Address)
			}
		} else {
			logging.Warn("Failed to get balance for participant", types.Participants, "address", p.Address, "error", err)
		}
		participants[i] = ParticipantDto{
			Id:          p.Address,
			Url:         p.InferenceUrl,
			CoinsOwed:   p.CoinBalance,
			Balance:     pBalance,
			VotingPower: int64(p.Weight),
		}
	}
	return ctx.JSON(http.StatusOK, &ParticipantsDto{
		Participants: participants,
		BlockHeight:  r.BlockHeight,
	})
}
