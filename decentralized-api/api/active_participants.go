package api

import (
	"context"
	storetypes "cosmossdk.io/store/types"
	"decentralized-api/apiconfig"
	cosmos_client "decentralized-api/cosmosclient"
	"encoding/hex"
	"github.com/cosmos/gogoproto/proto"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	cryptotypes "github.com/cometbft/cometbft/proto/tendermint/crypto"
	rpcclient "github.com/cometbft/cometbft/rpc/client"
	types2 "github.com/cometbft/cometbft/types"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/productscience/inference/x/inference/types"
)

type ActiveParticipantWithProof struct {
	ActiveParticipants      types.ActiveParticipants `json:"active_participants"`
	ActiveParticipantsBytes string                   `json:"active_participants_bytes"`
	ProofOps                cryptotypes.ProofOps     `json:"proof_ops"`
	Validators              []*types2.Validator      `json:"validators"`
	Block                   *types2.Block            `json:"block"`
}

func WrapGetParticipantsByEpoch(transactionRecorder cosmos_client.InferenceCosmosClient, config apiconfig.Config) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
			return
		}

		// Extract the path after '/v1/epochs/'
		path := strings.TrimPrefix(r.URL.Path, "/v1/epochs/")

		// Check if the path ends with '/participants'
		if !strings.HasSuffix(path, "/participants") {
			http.NotFound(w, r)
			return
		}

		// Remove the '/participants' suffix to get the epochId
		epochIdStr := strings.TrimSuffix(path, "/participants")

		// Ensure that there's no additional path segments
		if strings.ContainsRune(epochIdStr, '/') {
			http.NotFound(w, r)
			return
		}

		if epochIdStr == "current" {
			getParticipants(nil, w, config, transactionRecorder)
		} else {
			epochInt, err := strconv.Atoi(epochIdStr)
			if err != nil {
				http.Error(w, "Invalid epoch ID", http.StatusBadRequest)
				return
			}

			if epochInt < 0 {
				http.Error(w, "Invalid epoch ID", http.StatusBadRequest)
				return
			}

			epochUint := uint64(epochInt)
			getParticipants(&epochUint, w, config, transactionRecorder)
		}
	}
}

func getParticipants(epochOrNil *uint64, w http.ResponseWriter, config apiconfig.Config, transactionRecorder cosmos_client.InferenceCosmosClient) {
	rplClient, err := cosmos_client.NewRpcClient(config.ChainNode.Url)
	if err != nil {
		slog.Error("Failed to create rpc client", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	queryClient := transactionRecorder.NewInferenceQueryClient()
	currEpoch, err := queryClient.GetCurrentEpoch(transactionRecorder.Context, &types.QueryGetCurrentEpochRequest{})
	if err != nil {
		slog.Error("Failed to get current epoch", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("Current epoch resolved.", "epoch", currEpoch.Epoch)

	var epoch uint64
	if epochOrNil == nil {
		// /v1/epoch/current/participants
		epoch = currEpoch.Epoch
	} else {
		// /v1/epoch/{i}/participants
		if *epochOrNil > currEpoch.Epoch {
			http.Error(w, "Epoch not reached", http.StatusBadRequest)
			return
		}
		epoch = *epochOrNil
	}

	if epoch == 0 {
		http.Error(w, "Epoch enumeration starts with 1", http.StatusBadRequest)
		return
	}

	dataKey := string(types.ActiveParticipantsFullKey(epoch))
	result, err := cosmos_client.QueryByKey(rplClient, "inference", dataKey, true)
	if err != nil {
		slog.Error("Failed to query active participants", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	interfaceRegistry := codectypes.NewInterfaceRegistry()
	// Register interfaces used in your types
	types.RegisterInterfaces(interfaceRegistry)

	// Not sure if I need to do it or not?
	//interfaceRegistry.RegisterImplementations((*sdk.Msg)(nil),
	//	&storetypes.CommitInfo{},
	//)

	// Create the codec
	cdc := codec.NewProtoCodec(interfaceRegistry)

	var activeParticipants types.ActiveParticipants
	if err := cdc.Unmarshal(result.Response.Value, &activeParticipants); err != nil {
		slog.Error("Failed to unmarshal active participant", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	block, err := rplClient.Block(context.Background(), &activeParticipants.CreatedAtBlockHeight)
	if err != nil {
		slog.Error("Failed to get block", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	vals, err := rplClient.Validators(context.Background(), &activeParticipants.CreatedAtBlockHeight, nil, nil)
	if err != nil {
		slog.Error("Failed to get validators", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	commitInfoResponse, err := rplClient.ABCIQueryWithOptions(
		context.Background(),
		"/commit",
		nil,
		rpcclient.ABCIQueryOptions{Height: activeParticipants.CreatedAtBlockHeight, Prove: false},
	)
	if err != nil {
		slog.Error("Failed to get commit", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var commitInfo storetypes.CommitInfo
	if err := proto.Unmarshal(commitInfoResponse.Response.Value, &commitInfo); err != nil {
		slog.Error("Failed to unmarshal active participant", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	activeParticipantsBytes := hex.EncodeToString(result.Response.Value)

	response := ActiveParticipantWithProof{
		ActiveParticipants:      activeParticipants,
		ActiveParticipantsBytes: activeParticipantsBytes,
		ProofOps:                *result.Response.ProofOps,
		Validators:              vals.Validators,
		Block:                   block.Block,
	}

	RespondWithJson(w, response)
}
