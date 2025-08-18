package public

import (
	"context"
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/internal/utils"
	"decentralized-api/logging"
	"encoding/base64"
	"encoding/hex"
	"github.com/cometbft/cometbft/crypto/tmhash"
	rpcclient "github.com/cometbft/cometbft/rpc/client/http"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
	"strings"
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
	epochParam := c.Param("epoch")
	if epochParam == "" {
		return ErrInvalidEpochId
	}

	rpcClient, err := cosmos_client.NewRpcClient(s.configManager.GetChainNodeConfig().Url)
	if err != nil {
		logging.Error("Failed to create rpc client", types.System, "error", err)
		return err
	}

	activeParticipants, err := utils.QueryActiveParticipants(rpcClient, s.recorder.NewInferenceQueryClient())(context.Background(), epochParam)
	if err != nil {
		logging.Error("Failed to query active participants. Outer", types.Participants, "error", err)
		return err
	}

	return c.JSON(http.StatusOK, activeParticipants)
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

func queryActiveParticipants(rpcClient *rpcclient.HTTP, cdc *codec.ProtoCodec, epoch uint64) (*coretypes.ResultABCIQuery, error) {
	dataKey := types.ActiveParticipantsFullKey(epoch)
	result, err := cosmos_client.QueryByKey(rpcClient, "inference", dataKey)
	if err != nil {
		logging.Error("Failed to query active participants. Req 1", types.Participants, "error", err)
		return nil, err
	}

	logging.Info("[PARTICIPANTS-DEBUG] Raw active participants query result", types.Participants,
		"epoch", epoch,
		"value_bytes", len(result.Response.Value))

	if len(result.Response.Value) == 0 {
		logging.Error("Active participants query returned empty value", types.Participants, "epoch", epoch)
		return nil, echo.NewHTTPError(http.StatusNotFound, "No active participants found for the specified epoch. "+
			"Looks like PoC failed!")
	}

	var activeParticipants types.ActiveParticipants
	if err := cdc.Unmarshal(result.Response.Value, &activeParticipants); err != nil {
		logging.Error("Failed to unmarshal active participant. Req 1", types.Participants, "error", err)
		return nil, err
	}

	logging.Info("[PARTICIPANTS-DEBUG] Unmarshalled ActiveParticipants", types.Participants,
		"epoch", epoch,
		"created_at_block_height", activeParticipants.CreatedAtBlockHeight,
		"effective_block_height", activeParticipants.EffectiveBlockHeight)

	blockHeight := activeParticipants.CreatedAtBlockHeight
	queryWIthProof := true
	if blockHeight <= 1 {
		// cosmos can't build merkle proof for genesis because need previous state
		queryWIthProof = false
	}
	result, err = cosmos_client.QueryByKeyWithOptions(rpcClient, "inference", dataKey, blockHeight, queryWIthProof)
	if err != nil {
		logging.Error("Failed to query active participant. Req 2", types.Participants, "error", err)
		return nil, err
	}
	return result, err
}

func pubKeyToAddress3(pubKey string) (string, error) {
	pubKeyBytes, err := base64.StdEncoding.DecodeString(pubKey)
	if err != nil {
		return "", err
	}

	valAddr := tmhash.SumTruncated(pubKeyBytes)
	valAddrHex := strings.ToUpper(hex.EncodeToString(valAddr))
	return valAddrHex, nil
}
