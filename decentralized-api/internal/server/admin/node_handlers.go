package admin

import (
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/logging"
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
)

func (s *Server) getNodes(ctx echo.Context) error {
	nodes, err := s.nodeBroker.GetNodes()
	if err != nil {
		logging.Error("Error getting nodes", types.Nodes, "error", err)
		return err
	}
	return ctx.JSON(http.StatusOK, nodes)
}

func (s *Server) deleteNode(ctx echo.Context) error {
	nodeId := ctx.Param("id")
	logging.Info("Deleting node", types.Nodes, "node", nodeId)
	response := make(chan bool, 2)

	err := s.nodeBroker.QueueMessage(broker.RemoveNode{
		NodeId:   nodeId,
		Response: response,
	})
	if err != nil {
		logging.Error("Error deleting node", types.Nodes, "error", err)
		return err
	}
	node := <-response
	syncNodesWithConfig(s.nodeBroker, s.configManager)

	return ctx.JSON(http.StatusOK, node)
}

func syncNodesWithConfig(nodeBroker *broker.Broker, config *apiconfig.ConfigManager) {
	nodes, err := nodeBroker.GetNodes()
	iNodes := make([]apiconfig.InferenceNodeConfig, len(nodes))
	for i, n := range nodes {
		node := n.Node

		models := make(map[string]apiconfig.ModelConfig)
		for model, cfg := range node.Models {
			models[model] = apiconfig.ModelConfig{Args: cfg.Args}
		}

		iNodes[i] = apiconfig.InferenceNodeConfig{
			Host:             node.Host,
			InferenceSegment: node.InferenceSegment,
			InferencePort:    node.InferencePort,
			PoCSegment:       node.PoCSegment,
			PoCPort:          node.PoCPort,
			Models:           models,
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

func (s *Server) createNewNodes(ctx echo.Context) error {
	var newNodes []apiconfig.InferenceNodeConfig
	if err := ctx.Bind(&newNodes); err != nil {
		logging.Error("Error decoding request", types.Nodes, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	var outputNodes []apiconfig.InferenceNodeConfig
	for _, node := range newNodes {
		newNode, err := s.addNode(node)
		if err != nil {
			continue
		}
		outputNodes = append(outputNodes, newNode)
	}
	return ctx.JSON(http.StatusCreated, outputNodes)
}

func (s *Server) createNewNode(ctx echo.Context) error {
	var newNode apiconfig.InferenceNodeConfig
	if err := ctx.Bind(&newNode); err != nil {
		logging.Error("Error decoding request", types.Nodes, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	node, err := s.addNode(newNode)
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, node)
}

func (s *Server) addNode(newNode apiconfig.InferenceNodeConfig) (apiconfig.InferenceNodeConfig, error) {
	response := make(chan *apiconfig.InferenceNodeConfig, 2)
	err := s.nodeBroker.QueueMessage(broker.RegisterNode{
		Node:     newNode,
		Response: response,
	})
	if err != nil {
		return apiconfig.InferenceNodeConfig{}, err
	}

	node := <-response
	if node == nil {
		logging.Error("Error creating new node", types.Nodes, "error", err)
		return apiconfig.InferenceNodeConfig{}, errors.New("error creating new node")
	}

	newNodes := append(s.configManager.GetNodes(), *node)
	err = s.configManager.SetNodes(newNodes)
	if err != nil {
		logging.Error("Error writing config", types.Config, "error", err, "node", newNode.Id)
		return apiconfig.InferenceNodeConfig{}, err
	}

	return *node, nil
}

// enableNode handles POST /admin/v1/nodes/:id/enable
func (s *Server) enableNode(c echo.Context) error {
	nodeId := c.Param("id")
	if nodeId == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error": "node id is required",
		})
	}

	response := make(chan error, 2)
	err := s.nodeBroker.QueueMessage(broker.SetNodeAdminStateCommand{
		NodeId:   nodeId,
		Enabled:  true,
		Response: response,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to queue command: " + err.Error(),
		})
	}

	if err := <-response; err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "node enabled successfully",
		"node_id": nodeId,
	})
}

// disableNode handles POST /admin/v1/nodes/:id/disable
func (s *Server) disableNode(c echo.Context) error {
	nodeId := c.Param("id")
	if nodeId == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error": "node id is required",
		})
	}

	response := make(chan error, 2)
	err := s.nodeBroker.QueueMessage(broker.SetNodeAdminStateCommand{
		NodeId:   nodeId,
		Enabled:  false,
		Response: response,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to queue command: " + err.Error(),
		})
	}

	if err := <-response; err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "node disabled successfully",
		"node_id": nodeId,
	})
}
