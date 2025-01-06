package main

import (
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainevents"
	cosmosclient "decentralized-api/cosmosclient"
	"decentralized-api/poc"
	"decentralized-api/upgrade"
	"encoding/json"
	fmt "fmt"
	"github.com/gorilla/websocket"
	"github.com/productscience/inference/x/inference/proofofcompute"
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

func StartEventListener(
	nodeBroker *broker.Broker,
	transactionRecorder cosmosclient.InferenceCosmosClient,
	configManager *apiconfig.ConfigManager,
) {
	websocketUrl := getWebsocketUrl(configManager.GetConfig())
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
			slog.Warn("Failed to read a websocket message", "errorType", fmt.Sprintf("%T", err), "error", err)

			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				slog.Warn("Websocket connection closed", "errorType", fmt.Sprintf("%T", err), "error", err)
				if upgrade.CheckForUpgrade(configManager) {
					slog.Error("Upgrade required! Exiting...")
					panic("Upgrade required")
				}
				continue
			}
			continue
		}

		var event chainevents.JSONRPCResponse
		if err = json.Unmarshal(message, &event); err != nil {
			slog.Error("Error unmarshalling message to JSONRPCResponse", "error", err, "message", message)
		}

		switch event.Result.Data.Type {
		case "tendermint/event/NewBlock":
			slog.Debug("New block event received", "type", event.Result.Data.Type)
			poc.ProcessNewBlockEvent(pocOrchestrator, &event, transactionRecorder, configManager)
			upgrade.ProcessNewBlockEvent(&event, transactionRecorder, configManager)
		case "tendermint/event/Tx":
			go func() {
				handleMessage(nodeBroker, transactionRecorder, event, configManager.GetConfig())
			}()
		default:
			slog.Warn("Unexpected event type received", "type", event.Result.Data.Type)
		}
	}
}

func handleMessage(
	nodeBroker *broker.Broker,
	transactionRecorder cosmosclient.InferenceCosmosClient,
	event chainevents.JSONRPCResponse,
	currentConfig *apiconfig.Config,
) {
	if waitForEventHeight(event, currentConfig) {
		return
	}

	var action = event.Result.Events["message.action"][0]
	slog.Debug("New Tx event received", "type", event.Result.Data.Type, "action", action)
	// Get the keys of the map event.Result.Events:
	//for key := range event.Result.Events {
	//	for i, attr := range event.Result.Events[key] {
	//		slog.Debug("\tEventValue", "key", key, "attr", attr, "index", i)
	//	}
	//}
	switch action {
	case finishInferenceAction:
		SampleInferenceToValidate(event.Result.Events["inference_finished.inference_id"], transactionRecorder, nodeBroker, currentConfig)
	case validationAction:
		VerifyInvalidation(event.Result.Events, transactionRecorder, nodeBroker)
	}
}

// currentConfig must be a pointer, or it won't update
func waitForEventHeight(event chainevents.JSONRPCResponse, currentConfig *apiconfig.Config) bool {
	heightString := event.Result.Events["tx.height"][0]
	expectedHeight, err := strconv.ParseInt(heightString, 10, 64)
	if err != nil {
		slog.Error("Failed to parse height", "error", err)
		return true
	}
	for currentConfig.CurrentHeight < expectedHeight {
		slog.Info("Height race condition! Waiting for height to catch up", "currentHeight", currentConfig.CurrentHeight, "expectedHeight", expectedHeight)
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

func getWebsocketUrl(config *apiconfig.Config) string {
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
