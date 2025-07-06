package public

import (
	"context"
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"decentralized-api/merkleproof"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/cometbft/cometbft/crypto/tmhash"
	rpcclient "github.com/cometbft/cometbft/rpc/client/http"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	comettypes "github.com/cometbft/cometbft/types"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
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

func (s *Server) getParticipants(epoch uint64) (*ActiveParticipantWithProof, error) {
	// FIXME: now we can set active participants even for epoch 0, fix InitGenesis for that
	if epoch == 0 {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "Epoch enumeration starts with 1")
	}

	interfaceRegistry := codectypes.NewInterfaceRegistry()
	types.RegisterInterfaces(interfaceRegistry)

	cdc := codec.NewProtoCodec(interfaceRegistry)

	rpcClient, err := cosmos_client.NewRpcClient(s.configManager.GetChainNodeConfig().Url)
	if err != nil {
		logging.Error("Failed to create rpc client", types.System, "error", err)
		return nil, err
	}

	result, err := queryActiveParticipants(rpcClient, cdc, epoch)
	if err != nil {
		logging.Error("Failed to query active participants. Outer", types.Participants, "error", err)
		return nil, err
	}

	var activeParticipants types.ActiveParticipants
	if err := cdc.Unmarshal(result.Response.Value, &activeParticipants); err != nil {
		logging.Error("Failed to unmarshal active participant", types.Participants, "error", err)
		return nil, err
	}
	logging.Info("Active participants retrieved", types.Participants,
		"epoch", epoch,
		"activeParticipants", activeParticipants)

	block, err := rpcClient.Block(context.Background(), &activeParticipants.CreatedAtBlockHeight)
	if err != nil || block == nil {
		logging.Error("Failed to get block", types.Participants, "error", err)
		return nil, err
	}

	heightP1 := activeParticipants.CreatedAtBlockHeight + 1
	blockP1, err := rpcClient.Block(context.Background(), &heightP1)
	if err != nil || blockP1 == nil {
		logging.Error("Failed to get block + 1", types.Participants, "error", err)
	}

	heightM1 := activeParticipants.CreatedAtBlockHeight - 1
	blockM1, err := rpcClient.Block(context.Background(), &heightM1)
	if err != nil || blockM1 == nil {
		logging.Error("Failed to get block - 1", types.Participants, "error", err)
	}

	vals, err := rpcClient.Validators(context.Background(), &activeParticipants.CreatedAtBlockHeight, nil, nil)
	if err != nil || vals == nil {
		logging.Error("Failed to get validators", types.Participants, "error", err)
		return nil, err
	}

	if result.Response.ProofOps != nil {
		s.verifyProof(epoch, result, block)
	}

	activeParticipantsBytes := hex.EncodeToString(result.Response.Value)

	addresses := make([]string, len(activeParticipants.Participants))
	for i, participant := range activeParticipants.Participants {
		addresses[i], err = pubKeyToAddress3(participant.ValidatorKey)
		if err != nil {
			logging.Error("Failed to convert public key to address", types.Participants, "error", err)
		}
	}

	return &ActiveParticipantWithProof{
		ActiveParticipants:      activeParticipants,
		Addresses:               addresses,
		ActiveParticipantsBytes: activeParticipantsBytes,
		ProofOps:                result.Response.ProofOps,
		Validators:              vals.Validators,
		Block:                   []*comettypes.Block{block.Block, blockM1.Block, blockP1.Block},
	}, nil
}

func (s *Server) verifyProof(epoch uint64, result *coretypes.ResultABCIQuery, block *coretypes.ResultBlock) {
	dataKey := types.ActiveParticipantsFullKey(epoch)
	// Build the key path used by proof verification. We percent-encode the raw
	// binary key so the path is a valid UTF-8/URL string.
	verKey := "/inference/" + url.PathEscape(string(dataKey))
	// verKey2 := string(result.Response.Key)
	logging.Info("Attempting verification", types.Participants, "verKey", verKey)
	err := merkleproof.VerifyUsingProofRt(result.Response.ProofOps, block.Block.AppHash, verKey, result.Response.Value)
	if err != nil {
		logging.Info("VerifyUsingProofRt failed", types.Participants, "error", err)
	}

	err = merkleproof.VerifyUsingMerkleProof(result.Response.ProofOps, block.Block.AppHash, "inference", string(dataKey), result.Response.Value)
	if err != nil {
		logging.Info("VerifyUsingMerkleProof failed", types.Participants, "error", err)
	}
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

	var activeParticipants types.ActiveParticipants
	if err := cdc.Unmarshal(result.Response.Value, &activeParticipants); err != nil {
		logging.Error("Failed to unmarshal active participant. Req 1", types.Participants, "error", err)
		return nil, err
	}

	logging.Info("[PARTICIPANTS-DEBUG] Unmarshalled ActiveParticipants", types.Participants,
		"epoch", epoch,
		"created_at_block_height", activeParticipants.CreatedAtBlockHeight,
		"effective_block_height", activeParticipants.EffectiveBlockHeight)

	// We disable the second query with proof for now, because:
	// 1. Data migration happened, and we can't validate pre-migration records recursively;
	//    they are now signed by the validators active during the epoch.
	// 2. The implemented proof system has a bug anyway and needs to be revisited
	return result, nil

	/*	blockHeight := activeParticipants.CreatedAtBlockHeight
		result, err = cosmos_client.QueryByKeyWithOptions(rpcClient, "inference", dataKey, blockHeight, true)
		if err != nil {
			logging.Error("Failed to query active participant. Req 2", types.Participants, "error", err)
			return nil, err
		}

		return result, err*/
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
