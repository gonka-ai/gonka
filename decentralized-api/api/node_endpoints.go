package api

import (
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/logging"
	"encoding/json"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
	"strings"
)

func WrapNodes(nodeBroker *broker.Broker, configManager *apiconfig.ConfigManager) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		logging.Info("Request to nodes endpoint", types.Nodes, "method", request.Method)
		switch {
		case request.Method == http.MethodGet:
			getNodesResponse(nodeBroker, w)
			return
		case request.Method == http.MethodPost:
			if request.URL.Path == "/v1/nodes" {
				createNewNode(nodeBroker, w, request, configManager)
			} else if request.URL.Path == "/v1/nodes/batch" {
				createNewNodes(nodeBroker, w, request, configManager)
			} else {
				http.Error(w, "Invalid path", http.StatusNotFound)
			}
			return
		case request.Method == http.MethodDelete:
			deleteNode(nodeBroker, w, request, configManager)
			return
		default:
			http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
		}
	}
}

func deleteNode(nodeBroker *broker.Broker, w http.ResponseWriter, request *http.Request, configManager *apiconfig.ConfigManager) {
	// extract nodeid from url (not query params)
	nodeId := strings.TrimPrefix(request.URL.Path, "/v1/nodes/")
	logging.Info("Deleting node", types.Nodes, "node", nodeId)
	response := make(chan bool, 2)

	err := nodeBroker.QueueMessage(broker.RemoveNode{
		NodeId:   nodeId,
		Response: response,
	})
	if err != nil {
		logging.Error("Error deleting node", types.Nodes, "error", err)
		http.Error(w, "Error deleting node", http.StatusInternalServerError)
		return
	}
	node := <-response
	syncNodesWithConfig(nodeBroker, configManager)

	RespondWithJson(w, node)
}

func syncNodesWithConfig(nodeBroker *broker.Broker, config *apiconfig.ConfigManager) {
	nodes, err := nodeBroker.GetNodes()
	iNodes := make([]apiconfig.InferenceNodeConfig, len(nodes))
	for i, n := range nodes {
		node := *n.Node
		iNodes[i] = apiconfig.InferenceNodeConfig{
			Host:             node.Host,
			InferenceSegment: node.InferenceSegment,
			InferencePort:    node.InferencePort,
			PoCSegment:       node.PoCSegment,
			PoCPort:          node.PoCPort,
			Models:           node.Models,
			Id:               node.Id,
			MaxConcurrent:    node.MaxConcurrent,
			Hardware:         node.Hardware,
		}
	}
	err = config.SetNodes(iNodes)
	if err != nil {
		logging.Error("Error writing config", types.Nodes, "error", err)
	}
}

func createNewNodes(nodeBroker *broker.Broker, w http.ResponseWriter, request *http.Request, config *apiconfig.ConfigManager) {
	var newNodes []apiconfig.InferenceNodeConfig
	if err := json.NewDecoder(request.Body).Decode(&newNodes); err != nil {
		logging.Error("Error decoding request", types.Nodes, "error", err)
		http.Error(w, "Error decoding request", http.StatusBadRequest)
		return
	}
	var outputNodes []apiconfig.InferenceNodeConfig
	for _, node := range newNodes {
		newNode, done := addNode(nodeBroker, w, node, config)
		if done {
			return
		}
		outputNodes = append(outputNodes, newNode)
	}
	RespondWithJson(w, outputNodes)
}

func createNewNode(nodeBroker *broker.Broker, w http.ResponseWriter, request *http.Request, config *apiconfig.ConfigManager) {
	var newNode apiconfig.InferenceNodeConfig
	if err := json.NewDecoder(request.Body).Decode(&newNode); err != nil {
		logging.Error("Error decoding request", types.Nodes, "error", err)
		http.Error(w, "Error decoding request", http.StatusBadRequest)
		return
	}
	node, done := addNode(nodeBroker, w, newNode, config)
	if done {
		return
	}
	RespondWithJson(w, node)
}

func addNode(
	nodeBroker *broker.Broker,
	w http.ResponseWriter,
	newNode apiconfig.InferenceNodeConfig,
	configManager *apiconfig.ConfigManager,
) (apiconfig.InferenceNodeConfig, bool) {
	response := make(chan apiconfig.InferenceNodeConfig, 2)
	err := nodeBroker.QueueMessage(broker.RegisterNode{
		Node:     newNode,
		Response: response,
	})
	if err != nil {
		logging.Error("Error creating new node", types.Nodes, "error", err)
		http.Error(w, "Error creating new node", http.StatusInternalServerError)
		return apiconfig.InferenceNodeConfig{}, true
	}
	node := <-response
	nodes := configManager.GetNodes()
	newNodes := append(nodes, node)
	err = configManager.SetNodes(newNodes)
	if err != nil {
		logging.Error("Error writing config", types.Config, "error", err)
	}
	return node, false
}

func getNodesResponse(nodeBroker *broker.Broker, w http.ResponseWriter) {
	nodes, err := nodeBroker.GetNodes()
	if err != nil {
		logging.Error("Error getting nodes", types.Nodes, "error", err)
		http.Error(w, "Error getting nodes", http.StatusInternalServerError)
		return
	}
	RespondWithJson(w, nodes)
}
