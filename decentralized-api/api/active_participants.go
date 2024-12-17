package api

import (
	"context"
	"crypto/sha256"
	"decentralized-api/apiconfig"
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/merkleproof"
	"encoding/base64"
	"encoding/hex"
	"github.com/cometbft/cometbft/crypto/tmhash"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	cmcryptoed "github.com/cometbft/cometbft/crypto/ed25519"
	cryptotypes "github.com/cometbft/cometbft/proto/tendermint/crypto"
	rpcclient "github.com/cometbft/cometbft/rpc/client/http"
	comettypes "github.com/cometbft/cometbft/types"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/productscience/inference/x/inference/types"
)

type ActiveParticipantWithProof struct {
	ActiveParticipants      types.ActiveParticipants `json:"active_participants"`
	Addresses               []string                 `json:"addresses"`
	ActiveParticipantsBytes string                   `json:"active_participants_bytes"`
	ProofOps                cryptotypes.ProofOps     `json:"proof_ops"`
	Validators              []*comettypes.Validator  `json:"validators"`
	Block                   []*comettypes.Block      `json:"block"`
	// CommitInfo              storetypes.CommitInfo    `json:"commit_info"`
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

	interfaceRegistry := codectypes.NewInterfaceRegistry()
	// Register interfaces used in your types
	types.RegisterInterfaces(interfaceRegistry)

	// Not sure if I need to do it or not?
	//interfaceRegistry.RegisterImplementations((*sdk.Msg)(nil),
	//	&storetypes.CommitInfo{},
	//)

	// Create the codec
	cdc := codec.NewProtoCodec(interfaceRegistry)

	rpcClient, err := cosmos_client.NewRpcClient(config.ChainNode.Url)
	if err != nil {
		slog.Error("Failed to create rpc client", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	result, err := queryActiveParticipants(rpcClient, cdc, epoch)
	if err != nil {
		slog.Error("Failed to query active participants. Outer", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var activeParticipants types.ActiveParticipants
	if err := cdc.Unmarshal(result.Response.Value, &activeParticipants); err != nil {
		slog.Error("Failed to unmarshal active participant", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	block, err := rpcClient.Block(context.Background(), &activeParticipants.CreatedAtBlockHeight)
	if err != nil {
		slog.Error("Failed to get block", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	heightP1 := activeParticipants.CreatedAtBlockHeight + 1
	blockP1, err := rpcClient.Block(context.Background(), &heightP1)
	if err != nil {
		slog.Error("Failed to get block", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	heightM1 := activeParticipants.CreatedAtBlockHeight - 1
	blockM1, err := rpcClient.Block(context.Background(), &heightM1)
	if err != nil {
		slog.Error("Failed to get block", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	vals, err := rpcClient.Validators(context.Background(), &activeParticipants.CreatedAtBlockHeight, nil, nil)
	if err != nil {
		slog.Error("Failed to get validators", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	activeParticipantsBytes := hex.EncodeToString(result.Response.Value)

	dataKey := string(types.ActiveParticipantsFullKey(epoch))
	verKey := "/inference/" + url.PathEscape(dataKey)
	// verKey2 := string(result.Response.Key)
	slog.Info("Attempting verification", "verKey", verKey)
	err = merkleproof.VerifyUsingProofRt(result.Response.ProofOps, block.Block.AppHash, verKey, result.Response.Value)
	if err != nil {
		slog.Info("VerifyUsingProofRt failed", "error", err)
	}

	err = merkleproof.VerifyUsingMerkleProof(result.Response.ProofOps, block.Block.AppHash, "inference", dataKey, result.Response.Value)
	if err != nil {
		slog.Info("VerifyUsingMerkleProof failed", "error", err)
	}

	addresses := make([]string, len(activeParticipants.Participants))
	for i, participant := range activeParticipants.Participants {
		addresses[i], err = pubKeyToAddress3(participant.ValidatorKey)
		if err != nil {
			slog.Error("Failed to convert public key to address", "error", err)
		}
	}

	response := ActiveParticipantWithProof{
		ActiveParticipants:      activeParticipants,
		Addresses:               addresses,
		ActiveParticipantsBytes: activeParticipantsBytes,
		ProofOps:                *result.Response.ProofOps,
		Validators:              vals.Validators,
		Block:                   []*comettypes.Block{block.Block, blockM1.Block, blockP1.Block},
	}

	RespondWithJson(w, response)
}

func queryActiveParticipants(rpcClient *rpcclient.HTTP, cdc *codec.ProtoCodec, epoch uint64) (*coretypes.ResultABCIQuery, error) {
	dataKey := string(types.ActiveParticipantsFullKey(epoch))
	result, err := cosmos_client.QueryByKey(rpcClient, "inference", dataKey)
	if err != nil {
		slog.Error("Failed to query active participants. Req 1", "error", err)
		return nil, err
	}

	var activeParticipants types.ActiveParticipants
	if err := cdc.Unmarshal(result.Response.Value, &activeParticipants); err != nil {
		slog.Error("Failed to unmarshal active participant. Req 1", "error", err)
		return nil, err
	}

	blockHeight := activeParticipants.CreatedAtBlockHeight
	result, err = cosmos_client.QueryByKeyWithOptions(rpcClient, "inference", dataKey, blockHeight, true)
	if err != nil {
		slog.Error("Failed to query active participant. Req 2", "error", err)
		return nil, err
	}

	return result, err
}

func pubKeyToAddress(pubKey string) (string, error) {
	pubKeyBytes, err := base64.StdEncoding.DecodeString(pubKey)
	if err != nil {
		return "", err
	}

	hash := sha256.Sum256(pubKeyBytes)

	valAddr := hash[:20]

	addressHex := strings.ToUpper(hex.EncodeToString(valAddr))

	return addressHex, nil
}

func pubKeyToAddress2(pubKeyString string) (string, error) {
	pubKeyBytes, err := base64.StdEncoding.DecodeString(pubKeyString)
	if err != nil {
		return "", err
	}

	slog.Info("PubKey size", "len", len(pubKeyBytes))

	pubKey := cmcryptoed.PubKey(pubKeyBytes)

	valAddr := pubKey.Address()

	valAddrHex := strings.ToUpper(hex.EncodeToString(valAddr))

	return valAddrHex, nil
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
