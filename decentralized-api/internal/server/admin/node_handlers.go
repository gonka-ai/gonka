package admin

import (
	"common/logging"
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
)

func (s *Server) getNodes(ctx echo.Context) error {
	nodes, err := s.nodeBroker.GetNodes()
	if err != nil {
		logging.Error("Error getting nodes", types.Nodes, "error", err)
		return err
	}

	enriched := make([]NodeWithOnboarding, len(nodes))
	for i, n := range nodes {
		enriched[i] = NodeWithOnboarding{
			NodeResponse: n,
			Onboarding:   s.computeOnboarding(n),
		}
	}
	return ctx.JSON(http.StatusOK, enriched)
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
	if s.tester != nil {
		s.tester.Invalidate(nodeId)
	}

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
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
	}

	var outputNodes []apiconfig.InferenceNodeConfig
	var errors []string
	for i, node := range newNodes {
		newNode, err := s.addNode(node)
		if err != nil {
			errorMsg := fmt.Sprintf("node[%d] (id: %s): %v", i, node.Id, err)
			errors = append(errors, errorMsg)
			logging.Error("Failed to add node in batch", types.Nodes, "index", i, "node_id", node.Id, "error", err)
			continue
		}
		outputNodes = append(outputNodes, newNode)
	}

	if len(errors) > 0 && len(outputNodes) == 0 {
		// All nodes failed
		return echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{
			"error":  "all nodes failed validation",
			"errors": errors,
		})
	}

	if len(errors) > 0 {
		// Some nodes succeeded, some failed
		return ctx.JSON(http.StatusPartialContent, map[string]interface{}{
			"nodes":  outputNodes,
			"errors": errors,
		})
	}

	return ctx.JSON(http.StatusCreated, outputNodes)
}

func (s *Server) createNewNode(ctx echo.Context) error {
	var newNode apiconfig.InferenceNodeConfig
	if err := ctx.Bind(&newNode); err != nil {
		logging.Error("Error decoding request", types.Nodes, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
	}

	// Upsert: if node exists, update it; otherwise, create
	nodes, err := s.nodeBroker.GetNodes()
	if err != nil {
		logging.Error("Error reading nodes", types.Nodes, "error", err)
		return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("failed to read nodes: %v", err))
	}

	exists := false
	for _, n := range nodes {
		if n.Node.Id == newNode.Id {
			exists = true
			break
		}
	}

	if exists {
		command := broker.NewUpdateNodeCommand(newNode)
		err := s.nodeBroker.QueueMessage(command)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("failed to queue update command: %v", err))
		}
		response := <-command.Response
		if response.Error != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("failed to update node: %v", response.Error))
		}
		node := response.Node
		if node == nil {
			// Model check failed - validation already passed above
			return echo.NewHTTPError(http.StatusBadRequest, "failed to update node: one or more models are not valid governance models. Check logs for details.")
		}
		// sync config file with updated node list
		syncNodesWithConfig(s.nodeBroker, s.configManager)
		if s.tester != nil {
			s.tester.Invalidate(node.Id)
		}
		// Config changed — re-test if PoC is far enough away.
		s.maybeAutoTest(node.Id)
		return ctx.JSON(http.StatusOK, node)
	} else {
		node, err := s.addNode(newNode)
		if err != nil {
			return err
		}
		return ctx.JSON(http.StatusOK, node)
	}
}

func (s *Server) addNode(newNode apiconfig.InferenceNodeConfig) (apiconfig.InferenceNodeConfig, error) {
	// Validate before queuing to provide clear error messages to API users
	cmd := broker.NewRegisterNodeCommand(newNode)
	err := s.nodeBroker.QueueMessage(cmd)
	if err != nil {
		return apiconfig.InferenceNodeConfig{}, echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("failed to queue register command: %v", err))
	}

	response := <-cmd.Response
	if response.Error != nil {
		logging.Error("Error creating new node", types.Nodes, "error", response.Error, "node_id", newNode.Id)
		return apiconfig.InferenceNodeConfig{}, echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("failed to create node: %v", response.Error))
	}

	node := response.Node
	if node == nil {
		// Model check failed - validation already passed above
		logging.Error("Error creating new node - model validation failed", types.Nodes, "node_id", newNode.Id)
		return apiconfig.InferenceNodeConfig{}, echo.NewHTTPError(http.StatusBadRequest, "failed to create node: one or more models are not valid governance models. Check logs for details.")
	}

	newNodes := append(s.configManager.GetNodes(), *node)
	err = s.configManager.SetNodes(newNodes)
	if err != nil {
		logging.Error("Error writing config", types.Config, "error", err, "node", newNode.Id)
		return apiconfig.InferenceNodeConfig{}, echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("failed to save node configuration: %v", err))
	}

	if s.tester != nil {
		s.tester.Invalidate(node.Id)
	}
	// Auto-test the freshly registered node if PoC is far enough away.
	s.maybeAutoTest(node.Id)
	// Make "waiting for PoC" the visible log right after registration.
	s.logOnboardingStatus(false)

	return *node, nil
}

// postNodeTest handles POST /admin/v1/nodes/:id/test by running a
// synchronous one-shot validation against the configured MLnode.
// Does not mutate broker state — the response carries the result
// for the caller to display.
//
// Status codes:
//
//	200 — test completed (body contains TestResult, possibly with status=FAILED)
//	404 — node id not in configManager
//	409 — a test is already running, or the node is serving in the epoch
func (s *Server) postNodeTest(c echo.Context) error {
	nodeId := c.Param("id")
	if nodeId == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "node id is required"})
	}
	if s.tester == nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "tester not initialized"})
	}

	// 404 before any safety gate so an unknown node never looks merely
	// "blocked" (the gates below are node-agnostic / timing-based).
	if !s.tester.HasNode(nodeId) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": ErrNodeNotFound.Error(), "node_id": nodeId})
	}

	// Refuse to test a node involved in broker-managed work: the test reloads
	// models and then stops the node outside the broker's state machine.
	if reason := s.nodeTestBlockReason(nodeId); reason != "" {
		return c.JSON(http.StatusConflict, map[string]string{
			"error":   reason + "; testing would take it out of service",
			"node_id": nodeId,
		})
	}

	// Refuse when the node should already be online for an imminent/active PoC.
	// Unlike auto-test, the manual endpoint has no timing gate of its own, so
	// an operator could trigger a multi-minute test right as PoC starts; the
	// point-in-time idle check above cannot protect a node assigned while the
	// test is still running. An unknown schedule is left to operator discretion.
	if reason := s.pocImminentTestBlockReason(); reason != "" {
		return c.JSON(http.StatusConflict, map[string]string{
			"error":   reason,
			"node_id": nodeId,
		})
	}

	// Per-call timeout: model load + health probe is expected to
	// complete well under this. Clients can retry on timeout.
	ctx, cancel := context.WithTimeout(c.Request().Context(), 5*time.Minute)
	defer cancel()

	result, err := s.tester.Run(ctx, nodeId)
	switch {
	case errors.Is(err, ErrNodeNotFound):
		return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error(), "node_id": nodeId})
	case errors.Is(err, ErrTestInProgress):
		return c.JSON(http.StatusConflict, map[string]string{"error": err.Error(), "node_id": nodeId})
	case err != nil:
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, result)
}

// enableNode handles POST /admin/v1/nodes/:id/enable
func (s *Server) enableNode(c echo.Context) error {
	nodeId := c.Param("id")
	if nodeId == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "node id is required")
	}

	response := make(chan error, 2)
	err := s.nodeBroker.QueueMessage(broker.SetNodeAdminStateCommand{
		NodeId:   nodeId,
		Enabled:  true,
		Response: response,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to queue command: "+err.Error())
	}

	if err := <-response; err != nil {
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
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
		return echo.NewHTTPError(http.StatusBadRequest, "node id is required")
	}

	response := make(chan error, 2)
	err := s.nodeBroker.QueueMessage(broker.SetNodeAdminStateCommand{
		NodeId:   nodeId,
		Enabled:  false,
		Response: response,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to queue command: "+err.Error())
	}

	if err := <-response; err != nil {
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "node disabled successfully",
		"node_id": nodeId,
	})
}

// exportDb returns a human-readable JSON snapshot of DB-backed dynamic config
func (s *Server) exportDb(c echo.Context) error {
	ctx := c.Request().Context()
	db := s.configManager.SqlDb()
	if db == nil || db.GetDb() == nil {
		logging.Error("DB not initialized", types.Nodes)
		return echo.NewHTTPError(http.StatusInternalServerError, "db not initialized")
	}
	payload, err := apiconfig.ExportAllDb(ctx, db.GetDb())
	if err != nil {
		logging.Error("Failed to export DB state", types.Nodes, "error", err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, payload)
}
