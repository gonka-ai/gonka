package main

import (
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"encoding/json"
	"log/slog"
	"net/http"
	strings "strings"
)

func wrapNodes(nodeBroker *broker.Broker, config apiconfig.Config) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		slog.Info("Request to nodes endpoint", "method", request.Method)
		switch {
		case request.Method == http.MethodGet:
			getNodesResponse(nodeBroker, w, request, config)
			return
		case request.Method == http.MethodPost:
			if request.URL.Path == "/v1/nodes" {
				createNewNode(nodeBroker, w, request, config)
			} else if request.URL.Path == "/v1/nodes/batch" {
				createNewNodes(nodeBroker, w, request, config)
			} else {
				http.Error(w, "Invalid path", http.StatusNotFound)
			}
			return
		case request.Method == http.MethodDelete:
			deleteNode(nodeBroker, w, request, config)
			return
		default:
			http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
		}
	}
}

func deleteNode(nodeBroker *broker.Broker, w http.ResponseWriter, request *http.Request, config apiconfig.Config) {
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
	SyncNodesWithConfig(nodeBroker, config)

	respondWithJson(w, node)
}

func SyncNodesWithConfig(nodeBroker *broker.Broker, config apiconfig.Config) {
	nodes, err := nodeBroker.GetNodes()
	iNodes := make([]broker.InferenceNode, len(nodes))
	for i, n := range nodes {
		iNodes[i] = *n.Node
	}
	config.Nodes = iNodes
	err = apiconfig.WriteConfig(config)
	if err != nil {
		slog.Error("Error writing config", "error", err)
	}
}

func createNewNodes(nodeBroker *broker.Broker, w http.ResponseWriter, request *http.Request, config apiconfig.Config) {
	var newNodes []broker.InferenceNode
	if err := json.NewDecoder(request.Body).Decode(&newNodes); err != nil {
		slog.Error("Error decoding request", "error", err)
		http.Error(w, "Error decoding request", http.StatusBadRequest)
		return
	}
	var outputNodes []broker.InferenceNode
	for _, node := range newNodes {
		newNode, done := addNode(nodeBroker, w, node, config)
		if done {
			return
		}
		outputNodes = append(outputNodes, newNode)
	}
	respondWithJson(w, outputNodes)
}

func createNewNode(nodeBroker *broker.Broker, w http.ResponseWriter, request *http.Request, config apiconfig.Config) {
	var newNode broker.InferenceNode
	if err := json.NewDecoder(request.Body).Decode(&newNode); err != nil {
		slog.Error("Error decoding request", "error", err)
		http.Error(w, "Error decoding request", http.StatusBadRequest)
		return
	}
	node, done := addNode(nodeBroker, w, newNode, config)
	if done {
		return
	}
	respondWithJson(w, node)
}

func addNode(nodeBroker *broker.Broker, w http.ResponseWriter, newNode broker.InferenceNode, config apiconfig.Config) (broker.InferenceNode, bool) {
	response := make(chan broker.InferenceNode, 2)
	err := nodeBroker.QueueMessage(broker.RegisterNode{
		Node:     newNode,
		Response: response,
	})
	if err != nil {
		slog.Error("Error creating new node", "error", err)
		http.Error(w, "Error creating new node", http.StatusInternalServerError)
		return broker.InferenceNode{}, true
	}
	node := <-response
	config.Nodes = append(config.Nodes, node)
	err = apiconfig.WriteConfig(config)
	if err != nil {
		slog.Error("Error writing config", "error", err)
	}
	return node, false
}

func getNodesResponse(nodeBroker *broker.Broker, w http.ResponseWriter, request *http.Request, config apiconfig.Config) {
	nodes, err := nodeBroker.GetNodes()
	if err != nil {
		slog.Error("Error getting nodes", "error", err)
		http.Error(w, "Error getting nodes", http.StatusInternalServerError)
		return
	}
	respondWithJson(w, nodes)
}
