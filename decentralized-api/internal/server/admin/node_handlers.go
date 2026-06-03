package admin

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/logging"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
)

// NodeWithOnboarding is the admin GET /nodes response item. It wraps
// the broker's view of a node with onboarding UX fields that are
// derived at the handler layer (not stored on broker state).
type NodeWithOnboarding struct {
	broker.NodeResponse
	Onboarding *OnboardingStatus `json:"onboarding,omitempty"`
}

// OnboardingStatus aggregates everything the admin UI needs to show
// human-friendly setup state without inspecting raw broker internals.
type OnboardingStatus struct {
	ParticipantState string                `json:"participant_state"`
	MLNodeState      MLNodeOnboardingState `json:"mlnode_state"`
	Timing           *TimingInfo           `json:"timing,omitempty"`
	UserMessage      string                `json:"user_message,omitempty"`
	Guidance         string                `json:"guidance,omitempty"`
}

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

// computeOnboarding derives onboarding UX fields for one node from
// the broker's NodeResponse + cached participant activity + the
// chain phase tracker. It performs no chain RPC calls and does not
// mutate broker state. Returns nil if neither timing nor activity
// is known yet (e.g. chain not yet synced and tracker not started).
func (s *Server) computeOnboarding(n broker.NodeResponse) *OnboardingStatus {
	var epochState *chainphase.EpochState
	if s.phaseTracker != nil {
		epochState = s.phaseTracker.GetCurrentEpochState()
	}
	timing := ComputeTiming(epochState)
	if timing == nil && s.activityTracker == nil {
		return nil
	}

	active := false
	if s.activityTracker != nil {
		active = s.activityTracker.IsActive()
	}
	// EpochMLNodes already-populated also implies activity for this
	// participant in the current epoch; treat as a secondary signal.
	if !active && len(n.State.EpochMLNodes) > 0 {
		active = true
	}

	participantState := DeriveParticipantState(active)

	// Use a sentinel (not 0) when the schedule is unknown so downstream
	// helpers never render a bogus "PoC starting soon (in 0s)".
	seconds := SecondsUntilPoCUnknown
	if timing != nil {
		seconds = timing.SecondsUntilNextPoC
	}

	// Onboarding test signal is owned by the MLNodeTester, not the
	// broker: IsTesting while a one-shot test is in flight, TEST_FAILED
	// (with the offending model) from the most recent failed result. A
	// broker-side FAILED status is kept as a secondary signal so genuine
	// operational failures still surface even without a recent test.
	// TEST_FAILED is derived ONLY from the MLnode validation test, never
	// from the broker's operational FAILED status. An inactive
	// participant's node is driven to INFERENCE and "fails" with
	// no-epoch-models — that is normal onboarding, not a test failure, so
	// surfacing it as TEST_FAILED would contradict the proposal.
	isTesting := false
	testFailed := false
	failingModel := ""
	// validated == the node's most recent test passed. Gates the
	// reassuring "waiting for PoC" wording per the proposal.
	validated := false
	if s.tester != nil {
		nodeId := n.Node.Id
		isTesting = s.tester.IsRunning(nodeId)
		if last := s.tester.LastResult(nodeId); last != nil {
			switch last.Status {
			case TestFailed:
				testFailed = true
				failingModel = last.FailingModel
			case TestSuccess:
				validated = true
			}
		}
	}
	// An active participant's node is already proven (selected and serving
	// in the current epoch), so treat it as validated for the user-facing
	// wording rather than showing "not yet validated".
	if active {
		validated = true
	}

	mlState, _ := DeriveMLNodeState(OnboardingStateInputs{
		ParticipantActive:   active,
		IsTesting:           isTesting,
		TestFailed:          testFailed,
		SecondsUntilNextPoC: seconds,
	})

	// Lead with the MLnode-level message when the participant is active
	// OR when there is a test signal to report — a test in progress or a
	// failed test matters even before the participant joins the active
	// set. Otherwise lead with participant-level onboarding guidance.
	var userMsg, guidance string
	if active || mlState == MLNodeState_TESTING || mlState == MLNodeState_TEST_FAILED {
		userMsg = BuildMLNodeMessage(mlState, seconds, failingModel, validated)
		guidance = BuildParticipantMessage(participantState)
	} else {
		userMsg = BuildParticipantMessage(participantState)
		guidance = BuildInactiveGuidance(seconds)
	}

	return &OnboardingStatus{
		ParticipantState: string(participantState),
		MLNodeState:      mlState,
		Timing:           timing,
		UserMessage:      userMsg,
		Guidance:         guidance,
	}
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

	// Auto-test the freshly registered node if PoC is far enough away.
	s.maybeAutoTest(node.Id)
	// Make "waiting for PoC" the visible log right after registration.
	s.logOnboardingStatus(false)

	return *node, nil
}

// shouldAutoTest reports whether a node should be auto-tested right now.
// We only auto-test when the next PoC is more than
// AutoTestMinSecondsBeforePoC away, so loading models for a validation
// run never disturbs an imminent PoC. An unknown schedule
// (SecondsUntilPoCUnknown / negative) returns false.
func shouldAutoTest(secondsUntilNextPoC int64) bool {
	return secondsUntilNextPoC > apiconfig.AutoTestMinSecondsBeforePoC
}

// maybeAutoTest fires a one-shot MLnode validation in the background
// after a node is registered or its config changes, implementing the
// proposal's "auto-test a new MLnode when there's >1h until PoC". It is
// fire-and-forget: the result is recorded on the MLNodeTester and
// surfaces through GET /nodes via computeOnboarding — nothing is written
// back to the broker. No-op when the schedule is unknown, PoC is near,
// or a test for this node is already running.
func (s *Server) maybeAutoTest(nodeId string) {
	if s.tester == nil || s.phaseTracker == nil {
		return
	}
	// Never auto-test while the participant is active: such a node is
	// serving inference, and the test would reload + Stop it outside the
	// broker's knowledge, briefly taking it out of service. Pre-PoC
	// validation is for onboarding (inactive) nodes only.
	if s.activityTracker != nil && s.activityTracker.IsActive() {
		return
	}
	timing := ComputeTiming(s.phaseTracker.GetCurrentEpochState())
	if timing == nil || !shouldAutoTest(timing.SecondsUntilNextPoC) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		result, err := s.tester.Run(ctx, nodeId)
		switch {
		case errors.Is(err, ErrTestInProgress):
			// A test is already running for this node; nothing to do.
		case err != nil:
			logging.Debug("Auto-test could not run", types.Nodes, "node_id", nodeId, "error", err)
		case result != nil && result.Status == TestFailed:
			// Report the failure prominently in the API node logs so the
			// operator can fix it before PoC (proposal: detailed error
			// reporting on TEST_FAILED).
			logging.Error("Auto-test failed for MLnode before PoC", types.Nodes,
				"node_id", nodeId, "failing_model", result.FailingModel, "error", result.Error)
		case result != nil:
			logging.Info("Auto-test passed for MLnode", types.Nodes,
				"node_id", nodeId, "duration_ms", result.DurationMs)
		}
	}()
}

// postNodeTest handles POST /admin/v1/nodes/:id/test by running a
// synchronous one-shot validation against the configured MLnode.
// Does not mutate broker state — the response carries the result
// for the caller to display.
//
// Status codes:
//   200 — test completed (body contains TestResult, possibly with status=FAILED)
//   404 — node id not in configManager
//   409 — a test is already running for this node
func (s *Server) postNodeTest(c echo.Context) error {
	nodeId := c.Param("id")
	if nodeId == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "node id is required"})
	}
	if s.tester == nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "tester not initialized"})
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
