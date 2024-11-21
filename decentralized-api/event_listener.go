package main

import (
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainevents"
	cosmosclient "decentralized-api/cosmosclient"
	"decentralized-api/poc"
	"encoding/json"
	fmt "fmt"
	"github.com/gorilla/websocket"
	"github.com/productscience/inference/x/inference/proofofcompute"
	"github.com/productscience/inference/x/inference/utils"
	"log"
	"log/slog"
	"net/url"
	"strconv"
	"time"
)

const (
	finishInferenceAction = "/inference.inference.MsgFinishInference"
	validationAction      = "/inference.inference.MsgValidation"
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
	subscribeToEvents(ws, "tm.event='Tx' AND message.action='"+finishInferenceAction+"'")
	subscribeToEvents(ws, "tm.event='NewBlock'")
	subscribeToEvents(ws, "tm.event='Tx' AND inference_validation.needs_revalidation='true'")

	pubKey, err := transactionRecorder.Account.Record.GetPubKey()
	if err != nil {
		slog.Error("Failed to get public key", "error", err)
		return
	}
	pubKeyString := utils.PubKeyToHexString(pubKey)

	slog.Debug("Initializing PoC orchestrator",
		"name", transactionRecorder.Account.Name,
		"address", transactionRecorder.Address,
		"pubkey", pubKeyString)

	pocOrchestrator := poc.NewPoCOrchestrator(pubKeyString, proofofcompute.DefaultDifficulty)
	// PRTODO: decide if host is just host or host+port????? or url. Think what better name and stuff
	nodePocOrchestrator := poc.NewNodePoCOrchestrator(pubKeyString, nodeBroker, config.Api.Host, config.ChainNode.Url, &transactionRecorder)
	slog.Info("PoC orchestrator initialized", "nodePocOrchestrator", nodePocOrchestrator)
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
			poc.ProcessNewBlockEvent(pocOrchestrator, nodePocOrchestrator, &event, transactionRecorder)
		case "tendermint/event/Tx":
			go func() {
				handleMessage(nodeBroker, transactionRecorder, event)
			}()
		default:
			slog.Warn("Unexpected event type received", "type", event.Result.Data.Type)
		}
	}
}

func handleMessage(nodeBroker *broker.Broker, transactionRecorder cosmosclient.InferenceCosmosClient, event chainevents.JSONRPCResponse) {
	if waitForEventHeight(event) {
		return
	}

	var action = event.Result.Events["message.action"][0]
	slog.Debug("New Tx event received", "type", event.Result.Data.Type, "action", action)
	// Get the keys of the map event.Result.Events:
	for key := range event.Result.Events {
		for i, attr := range event.Result.Events[key] {
			slog.Debug("EventValue", "key", key, "attr", attr, "index", i)
		}
	}
	switch action {
	case finishInferenceAction:
		SampleInferenceToValidate(event.Result.Events["inference_finished.inference_id"], transactionRecorder, nodeBroker)
	case validationAction:
		VerifyInvalidation(event.Result.Events, transactionRecorder, nodeBroker)
	}
}

func waitForEventHeight(event chainevents.JSONRPCResponse) bool {
	heightString := event.Result.Events["tx.height"][0]
	expectedHeight, err := strconv.ParseInt(heightString, 10, 64)
	if err != nil {
		slog.Error("Failed to parse height", "error", err)
		return true
	}
	for poc.CurrentHeight < expectedHeight {
		slog.Info("Height race condition! Waiting for height to catch up", "currentHeight", poc.CurrentHeight, "expectedHeight", expectedHeight)
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func subscribeToEvents(ws *websocket.Conn, query string) {
	subscribeMsg := fmt.Sprintf(`{"jsonrpc": "2.0", "method": "subscribe", "id": "1", "params": ["%s"]}`, query)
	if err := ws.WriteMessage(websocket.TextMessage, []byte(subscribeMsg)); err != nil {
		slog.Error("Failed to subscribe to a websocket", "error", err)
		log.Fatalf("Failed to subscribe to a websocket. %v", err)
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
