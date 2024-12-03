package api

import (
	"context"
	"decentralized-api/apiconfig"
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/merkleproof"
	cryptotypes "github.com/cometbft/cometbft/proto/tendermint/crypto"
	types2 "github.com/cometbft/cometbft/types"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/productscience/inference/x/inference/types"
	"log/slog"
	"net/http"
)

type ActiveParticipantWithProof struct {
	ActiveParticipants types.ActiveParticipants `json:"active_participants"`
	ProofOps           cryptotypes.ProofOps     `json:"proof_ops"`
	Validators         []*types2.Validator      `json:"validators"`
	Block              *types2.Block            `json:"block"`
}

func WrapGetActiveParticipants(config apiconfig.Config) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
			return
		}

		rplClient, err := cosmos_client.NewRpcClient(config.ChainNode.Url)
		if err != nil {
			slog.Error("Failed to create rpc client", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}

		// PRTODO: insert epoch here!
		result, err := merkleproof.QueryWithProof(rplClient, "inference", "ActiveParticipants/value/")
		if err != nil {
			slog.Error("Failed to query active participants", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		interfaceRegistry := codectypes.NewInterfaceRegistry()
		// Register interfaces used in your types
		types.RegisterInterfaces(interfaceRegistry)
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

		response := ActiveParticipantWithProof{
			ActiveParticipants: activeParticipants,
			ProofOps:           *result.Response.ProofOps,
			Validators:         vals.Validators,
			Block:              block.Block,
		}

		RespondWithJson(w, response)
	}
}
