package api

import (
	"decentralized-api/merkleproof"
	"encoding/hex"
	"encoding/json"
	cryptotypes "github.com/cometbft/cometbft/proto/tendermint/crypto"
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
