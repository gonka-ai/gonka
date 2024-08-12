package main

import (
	"decentralized-api/broker"
	"encoding/json"
	"github.com/gorilla/websocket"
	"log"
	"net/url"
)

type Attribute struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Index bool   `json:"index"`
}

type Event struct {
	Type       string      `json:"type"`
	Attributes []Attribute `json:"attributes"`
}

type TxResult struct {
	Height string `json:"height"`
	Tx     string `json:"tx"`
	Result struct {
		Events []Event `json:"events"`
	} `json:"result"`
}

type Value struct {
	TxResult TxResult `json:"TxResult"`
}

type Data struct {
	Type  string `json:"type"`
	Value Value  `json:"value"`
}

type Result struct {
	Query  string              `json:"query"`
	Data   Data                `json:"data"`
	Events map[string][]string `json:"events"`
}

type JSONRPCResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Result  Result `json:"result"`
}

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

	// Listen for events
	for {
		_, message, err := ws.ReadMessage()
		if err != nil {
			log.Printf("Failed to read a websocket message. %v", err)
		}

		log.Printf("Received: %s", message)

		var txEvent JSONRPCResponse
		if err = json.Unmarshal(message, &txEvent); err != nil {
			log.Printf("Error unmarshalling message to JSONRPCResponse: %s", err)
		}

		go func() {
			SampleInferenceToValidate(txEvent.Result.Events["inference_finished.inference_id"], transactionRecorder, nodeBroker)
		}()
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
