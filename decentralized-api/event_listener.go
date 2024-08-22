package main

import (
	"decentralized-api/broker"
	"decentralized-api/chain_events"
	"decentralized-api/proof_of_compute"
	"encoding/json"
	"github.com/gorilla/websocket"
	"log"
	"net/url"
)

func StartEventListener(nodeBroker *broker.Broker, transactionRecorder InferenceCosmosClient, config Config) {
	websocketUrl := getWebsocketUrl(config)
	log.Printf("Connecting to websocket at %s", websocketUrl)
	ws, _, err := websocket.DefaultDialer.Dial(websocketUrl, nil)
	if err != nil {
		log.Fatal("dial:", err)
	}
	defer ws.Close()

	// Subscribe to custom events
	subscribeMsg := `{"jsonrpc": "2.0", "method": "subscribe", "id": "1", "params": ["tm.event='Tx' AND message.action='/inference.inference.MsgFinishInference'"]}`
	if err = ws.WriteMessage(websocket.TextMessage, []byte(subscribeMsg)); err != nil {
		log.Fatalf("Failed to subscribe to a websocket. %v", err)
	}

	subscribeMsg2 := `{"jsonrpc": "2.0", "method": "subscribe", "id": "2", "params": ["tm.event='NewBlock'"]}`
	if err = ws.WriteMessage(websocket.TextMessage, []byte(subscribeMsg2)); err != nil {
		log.Fatalf("Failed to subscribe to a websocket. %v", err)
	}

	pubKey, err := transactionRecorder.account.PubKey()
	if err != nil {
		log.Fatalf("Failed to get public key. %v", err)
		return
	}
	powOrchestrator := proof_of_compute.NewPowOrchestrator(pubKey)
	go powOrchestrator.Run()

	// Listen for events
	for {
		_, message, err := ws.ReadMessage()
		if err != nil {
			log.Printf("Failed to read a websocket message. %v", err)
		}

		var event chain_events.JSONRPCResponse
		if err = json.Unmarshal(message, &event); err != nil {
			log.Printf("Error unmarshalling message to JSONRPCResponse. err = %s. message = %s", err, message)
		}

		switch event.Result.Data.Type {
		case "tendermint/event/NewBlock":
			log.Printf("New block event received. type = %s", event.Result.Data.Type)
			proof_of_compute.ProcessNewBlockEvent(powOrchestrator, &event)
		default:
			log.Printf("New Tx event received. Inference finished. type = %s", event.Result.Data.Type)
			go func() {
				SampleInferenceToValidate(event.Result.Events["inference_finished.inference_id"], transactionRecorder, nodeBroker)
			}()
		}
	}
}

func getWebsocketUrl(config Config) string {
	// Parse the input URL
	u, err := url.Parse(config.ChainNode.Url)
	if err != nil {
		log.Printf("Error parsing URL: %s", err)
		return ""
	}

	// Modify the scheme to "ws" and append the "/websocket" path
	u.Scheme = "ws"
	u.Path = "/websocket"

	// Construct the new URL
	return u.String()
}
