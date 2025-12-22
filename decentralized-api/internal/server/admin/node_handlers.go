package admin

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/logging"
	"fmt"
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
	osm := NewOnboardingStateManager()
	sr := NewStatusReporter()

	for i := range nodes {
		state := &nodes[i].State
		participantActive := len(state.EpochMLNodes) > 0
		pstate := osm.ParticipantStatus(participantActive)
		state.ParticipantState = string(pstate)

		var secs int64
		if state.Timing != nil {
			secs = state.Timing.SecondsUntilNextPoC
			sr.LogTimingGuidance(secs)
		}

		isTesting := false
		testFailed := state.FailureReason != ""
		if !participantActive {
			isTesting = false
			testFailed = false
		}
		mlnodeState, _, _ := osm.MLNodeStatusSimple(secs, isTesting, testFailed)
		var userMsg string
		if participantActive {
			userMsg = sr.BuildMLNodeMessage(mlnodeState, secs, "")
		}

		state.MLNodeOnboardingState = string(mlnodeState)
		state.UserMessage = userMsg
		state.Guidance = sr.BuildParticipantMessage(pstate)
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

	// Auto-test trigger: run pre-PoC validation if timing allows (>1h until next PoC)
	// Fetch timing info from broker
	getCmd := broker.NewGetNodesCommand()
	if err := s.nodeBroker.QueueMessage(getCmd); err == nil {
		responses := <-getCmd.Response
		var secs int64
		for _, resp := range responses {
			if resp.Node.Id == newNode.Id && resp.State.Timing != nil {
				secs = resp.State.Timing.SecondsUntilNextPoC
				break
			}
		}
		if s.tester.ShouldAutoTest(secs) {
			s.statusReporter.LogTesting("Auto-testing MLnode configuration")
			result := s.tester.RunNodeTest(context.Background(), *node)
			if result != nil {
				if result.Status == TestFailed {
					cmd := broker.NewSetNodeFailureReasonCommand(newNode.Id, result.Error)
					_ = s.nodeBroker.QueueMessage(cmd)
				} else {
					// Clear any previous failure reason on success
					cmd := broker.NewSetNodeFailureReasonCommand(newNode.Id, "")
					_ = s.nodeBroker.QueueMessage(cmd)
				}
				s.latestTestResults[newNode.Id] = result
			}
		}
	}

	return *node, nil
}

// postNodeTest triggers a manual MLnode validation test for a specific node
func (s *Server) postNodeTest(ctx echo.Context) error {
	nodeId := ctx.Param("id")
	if nodeId == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "node id is required")
	}

	// Find node config by id
	var cfgNode *apiconfig.InferenceNodeConfig
	nodes := s.configManager.GetNodes()
	for i := range nodes {
		if nodes[i].Id == nodeId {
			cfgNode = &nodes[i]
			break
		}
	}
	if cfgNode == nil {
		return echo.NewHTTPError(http.StatusNotFound, fmt.Sprintf("node not found: %s", nodeId))
	}

	// Run test immediately (manual trigger ignores timing window)
	result := s.tester.RunNodeTest(context.Background(), *cfgNode)
	if result != nil {
		if result.Status == TestFailed {
			cmd := broker.NewSetNodeFailureReasonCommand(nodeId, result.Error)
			_ = s.nodeBroker.QueueMessage(cmd)
		} else {
			cmd := broker.NewSetNodeFailureReasonCommand(nodeId, "")
			_ = s.nodeBroker.QueueMessage(cmd)
		}
		s.latestTestResults[nodeId] = result
	}

	return ctx.JSON(http.StatusOK, result)
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
