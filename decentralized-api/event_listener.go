package main

import (
	"github.com/gorilla/websocket"
	"log"
	"net/url"
)

func StartEventListener(transactionRecorder InferenceCosmosClient, config Config) {
	websocketUrl := getWebsocketUrl(config)
	log.Printf("Connecting to websocket at %s", websocketUrl)
	ws, _, err := websocket.DefaultDialer.Dial(websocketUrl, nil)
	if err != nil {
		log.Fatal("dial:", err)
	}
	defer ws.Close()

	// Subscribe to custom events
	subscribeMsg := `{"jsonrpc": "2.0", "method": "subscribe", "id": "1", "params": ["tm.event='Tx' AND message.action='/inference.inference.MsgFinishInference'"]}`
	if err := ws.WriteMessage(websocket.TextMessage, []byte(subscribeMsg)); err != nil {
		log.Fatal("write:", err)
	}

	// Listen for events
	for {
		_, message, err := ws.ReadMessage()
		if err != nil {
			log.Fatal("read:", err)
		}
		log.Printf("Received: %s", message)
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
