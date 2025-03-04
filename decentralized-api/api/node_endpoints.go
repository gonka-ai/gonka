package api

import (
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

func WrapNodes(nodeBroker *broker.Broker, configManager *apiconfig.ConfigManager) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		slog.Info("Request to nodes endpoint", "method", request.Method)
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
	slog.Info("Deleting node", "node", nodeId)
	response := make(chan bool, 2)

	err := nodeBroker.QueueMessage(broker.RemoveNode{
		NodeId:   nodeId,
		Response: response,
	})
	if err != nil {
		slog.Error("Error deleting node", "error", err)
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
			Host:          node.Host,
			InferencePort: node.InferencePort,
			PoCPort:       node.PoCPort,
			Models:        node.Models,
			Id:            node.Id,
			MaxConcurrent: node.MaxConcurrent,
			Hardware:      node.Hardware,
		}
	}
	err = config.SetNodes(iNodes)
	if err != nil {
		slog.Error("Error writing config", "error", err)
	}
}

func createNewNodes(nodeBroker *broker.Broker, w http.ResponseWriter, request *http.Request, config *apiconfig.ConfigManager) {
	var newNodes []apiconfig.InferenceNodeConfig
	if err := json.NewDecoder(request.Body).Decode(&newNodes); err != nil {
		slog.Error("Error decoding request", "error", err)
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
		slog.Error("Error decoding request", "error", err)
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
		slog.Error("Error creating new node", "error", err)
		http.Error(w, "Error creating new node", http.StatusInternalServerError)
		return apiconfig.InferenceNodeConfig{}, true
	}
	node := <-response
	config := configManager.GetConfig()
	newNodes := append(config.Nodes, node)
	err = configManager.SetNodes(newNodes)
	if err != nil {
		slog.Error("Error writing config", "error", err)
	}
	return node, false
}

func getNodesResponse(nodeBroker *broker.Broker, w http.ResponseWriter) {
	nodes, err := nodeBroker.GetNodes()
	if err != nil {
		slog.Error("Error getting nodes", "error", err)
		http.Error(w, "Error getting nodes", http.StatusInternalServerError)
		return
	}
	RespondWithJson(w, nodes)
}
