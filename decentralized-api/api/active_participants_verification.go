package api

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/merkleproof"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	cmcryptoed "github.com/cometbft/cometbft/crypto/ed25519"
	cryptotypes "github.com/cometbft/cometbft/proto/tendermint/crypto"
	rpcclient "github.com/cometbft/cometbft/rpc/client/http"
	comettypes "github.com/cometbft/cometbft/types"
	"github.com/productscience/inference/x/inference/types"
	"log/slog"
	"net/http"
	"net/url"
)

type ProofVerificationRequest struct {
	Value    string               `json:"value"`
	AppHash  string               `json:"app_hash"`
	ProofOps cryptotypes.ProofOps `json:"proof_ops"`
	Epoch    int64                `json:"epoch"`
}

func WrapVerifyProof() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var proofVerificationRequest ProofVerificationRequest
		if err := json.NewDecoder(r.Body).Decode(&proofVerificationRequest); err != nil {
			slog.Error("Error decoding request", "error", err)
			http.Error(w, "Error decoding request", http.StatusBadRequest)
			return
		}

		dataKey := string(types.ActiveParticipantsFullKey(uint64(proofVerificationRequest.Epoch)))
		verKey := "/inference/" + url.PathEscape(dataKey)

		appHash, err := hex.DecodeString(proofVerificationRequest.AppHash)
		if err != nil {
			slog.Error("Error decoding app hash", "error", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		value, err := hex.DecodeString(proofVerificationRequest.Value)
		if err != nil {
			slog.Error("Error decoding value", "error", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		slog.Info("Attempting verification", "verKey", verKey, "appHash", appHash, "value", proofVerificationRequest.Value)
		err = merkleproof.VerifyUsingProofRt(&proofVerificationRequest.ProofOps, appHash, verKey, value)
		if err != nil {
			slog.Info("VerifyUsingProofRt failed", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}

		w.WriteHeader(http.StatusOK)
	}
}

type VerifyBlockRequest struct {
	Block      comettypes.Block `json:"block"`
	Validators []Validator      `json:"validators"`
}

type Validator struct {
	PubKey      string `json:"pub_key"`
	VotingPower int64  `json:"voting_power"`
}

func WrapVerifyBlock(config apiconfig.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var blockVerificationRequest VerifyBlockRequest
		if err := json.NewDecoder(r.Body).Decode(&blockVerificationRequest); err != nil {
			slog.Error("Error decoding request", "error", err)
			http.Error(w, "Error decoding request", http.StatusBadRequest)
			return
		}

		block := &blockVerificationRequest.Block

		valSet := make([]*comettypes.Validator, len(blockVerificationRequest.Validators))
		for i, validator := range blockVerificationRequest.Validators {
			pubKeyBytes, err := base64.StdEncoding.DecodeString(validator.PubKey)
			if err != nil {
				slog.Error("Error decoding public key", "error", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			pubKey := cmcryptoed.PubKey(pubKeyBytes)
			valSet[i] = comettypes.NewValidator(pubKey, validator.VotingPower)
		}

		err := debug(config.ChainNode.Url, block)
		if err != nil {
			slog.Error("Debug block verification failed!", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		slog.Info("Received validators", "height", block.Height, "valSet", valSet)

		err = merkleproof.VerifyCommit(block.Header.ChainID, block.LastCommit, &block.Header, valSet)
		if err != nil {
			slog.Error("Block signature verification failed", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
	}
}

func debug(address string, block *comettypes.Block) error {
	rpcClient, err := rpcclient.New(address, "/websocket")
	if err != nil {
		return err
	}

	valSetRes, err := rpcClient.Validators(context.Background(), &block.Height, nil, nil)
	if err != nil {
		return err
	}
	valSet := valSetRes.Validators

	slog.Info("Ground truth validators", "height", block.Height, "valSet", valSet)

	return merkleproof.VerifyCommit(block.Header.ChainID, block.LastCommit, &block.Header, valSet)
}
