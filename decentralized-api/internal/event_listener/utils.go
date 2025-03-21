package event_listener

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"fmt"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/gorilla/websocket"
	"github.com/productscience/inference/x/inference/types"
	"log"
	"net/url"
)

func subscribeToEvents(ws *websocket.Conn, query string) {
	subscribeMsg := fmt.Sprintf(`{"jsonrpc": "2.0", "method": "subscribe", "id": "1", "params": ["%s"]}`, query)
	if err := ws.WriteMessage(websocket.TextMessage, []byte(subscribeMsg)); err != nil {
		logging.Error("Failed to subscribe to a websocket", types.EventProcessing, "error", err)
		log.Fatalf("Failed to subscribe to a websocket. %v", err)
	}
}

func getWebsocketUrl(config *apiconfig.Config) string {
	u, err := url.Parse(config.ChainNode.Url)
	if err != nil {
		logging.Error("Error parsing URL", types.EventProcessing, "error", err)
		return ""
	}

	u.Scheme = "ws"
	u.Path = "/websocket"

	return u.String()
}

func getStatus(chainNodeUrl string) (*coretypes.ResultStatus, error) {
	client, err := cosmosclient.NewRpcClient(chainNodeUrl)
	if err != nil {
		return nil, err
	}

	return client.Status(context.Background())
}
