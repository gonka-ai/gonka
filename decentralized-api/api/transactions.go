package api

import (
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"encoding/json"
	"fmt"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/productscience/inference/x/inference/types"
	"io"
	"net/http"
)

func WrapSendTransaction(cosmosClient cosmosclient.CosmosMessageClient, cdc *codec.ProtoCodec) func(w http.ResponseWriter, request *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		logging.Debug("Received send transaction request", types.Messages)
		// Read request body
		body, err := io.ReadAll(request.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to read request body: %v", err), http.StatusBadRequest)
			return
		}

		// Unmarshal JSON into tx.Tx
		var tx txtypes.Tx
		if err := cdc.UnmarshalJSON(body, &tx); err != nil {
			http.Error(w, fmt.Sprintf("failed to unmarshal tx JSON: %v", err), http.StatusBadRequest)
			return
		}
		logging.Debug("Unmarshalled tx", types.Messages,
			"tx", tx,
		)

		// Extract and decode the first message
		if len(tx.Body.Messages) == 0 {
			http.Error(w, "no messages found in tx", http.StatusBadRequest)
			return
		}

		msgAny := tx.Body.Messages[0]
		var msg sdk.Msg
		if err := cdc.UnpackAny(msgAny, &msg); err != nil {
			http.Error(w, fmt.Sprintf("failed to unpack message: %v", err), http.StatusBadRequest)
			return
		}

		logging.Debug("Unpacked message", types.Messages, "Message", msg)

		txResp, err := cosmosClient.SendTransaction(msg.(sdk.Msg))
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to send transaction: %v", err), http.StatusInternalServerError)
			return
		}

		// Return transaction response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(txResp)
	}
}
