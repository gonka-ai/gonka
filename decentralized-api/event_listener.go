package main

import (
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainevents"
	cosmosclient "decentralized-api/cosmosclient"
	"decentralized-api/poc"
	"encoding/json"
	"github.com/gorilla/websocket"
	"github.com/productscience/inference/x/inference/proofofcompute"
	"log"
	"log/slog"
	"net/url"
)

func StartEventListener(nodeBroker *broker.Broker, transactionRecorder cosmosclient.InferenceCosmosClient, config apiconfig.Config) {
	websocketUrl := getWebsocketUrl(config)
	slog.Info("Connecting to websocket at", "url", websocketUrl)
	ws, _, err := websocket.DefaultDialer.Dial(websocketUrl, nil)
	if err != nil {
		slog.Error("Failed to connect to websocket", "error", err)
		log.Fatal("dial:", err)
	}
	defer ws.Close()

	// Subscribe to custom events
	subscribeMsg := `{"jsonrpc": "2.0", "method": "subscribe", "id": "1", "params": ["tm.event='Tx' AND message.action='/inference.inference.MsgFinishInference'"]}`
	if err = ws.WriteMessage(websocket.TextMessage, []byte(subscribeMsg)); err != nil {
		slog.Error("Failed to subscribe to a websocket", "error", err)
		log.Fatalf("Failed to subscribe to a websocket. %v", err)
	}

	subscribeMsg2 := `{"jsonrpc": "2.0", "method": "subscribe", "id": "2", "params": ["tm.event='NewBlock'"]}`
	if err = ws.WriteMessage(websocket.TextMessage, []byte(subscribeMsg2)); err != nil {
		slog.Error("Failed to subscribe to a websocket", "error", err)
		log.Fatalf("Failed to subscribe to a websocket. %v", err)
	}

	pubKey, err := transactionRecorder.Account.PubKey()
	if err != nil {
		slog.Error("Failed to get public key", "error", err)
		return
	}

	slog.Debug("Initializing PoC orchestrator",
		"name", transactionRecorder.Account.Name,
		"address", transactionRecorder.Address,
		"pubkey", pubKey)
	pocOrchestrator := poc.NewPoCOrchestrator(pubKey, proofofcompute.DefaultDifficulty)
	go pocOrchestrator.Run()

	// Listen for events
	for {
		_, message, err := ws.ReadMessage()
		if err != nil {
			slog.Warn("Failed to read a websocket message", "error", err)
		}

		var event chainevents.JSONRPCResponse
		if err = json.Unmarshal(message, &event); err != nil {
			slog.Error("Error unmarshalling message to JSONRPCResponse", "error", err, "message", message)
		}

		switch event.Result.Data.Type {
		case "tendermint/event/NewBlock":
			slog.Debug("New block event received", "type", event.Result.Data.Type)
			poc.ProcessNewBlockEvent(pocOrchestrator, &event, transactionRecorder)
		case "tendermint/event/Tx":
			slog.Debug("New Tx event received", "type", event.Result.Data.Type)
			go func() {
				SampleInferenceToValidate(event.Result.Events["inference_finished.inference_id"], transactionRecorder, nodeBroker)
			}()
		default:
			slog.Warn("Unexpected event type received", "type", event.Result.Data.Type)
		}
	}
}

func getWebsocketUrl(config apiconfig.Config) string {
	// Parse the input URL
	u, err := url.Parse(config.ChainNode.Url)
	if err != nil {
		slog.Error("Error parsing URL", "error", err)
		return ""
	}

	// Modify the scheme to "ws" and append the "/websocket" path
	u.Scheme = "ws"
	u.Path = "/websocket"

	// Construct the new URL
	return u.String()
}
