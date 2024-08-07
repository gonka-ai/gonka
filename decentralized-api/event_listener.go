package main

import (
	"github.com/gorilla/websocket"
	"log"
)

func StartEventListener(transactionRecorder InferenceCosmosClient, config Config) {
	ws, _, err := websocket.DefaultDialer.Dial("ws://localhost:26657/websocket", nil)
	if err != nil {
		log.Fatal("dial:", err)
	}
	defer ws.Close()

	// Subscribe to custom events
	subscribeMsg := `{"jsonrpc": "2.0", "method": "subscribe", "id": "1", "params": ["tm.event='Tx'"]}`
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
