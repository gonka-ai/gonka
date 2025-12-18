package admin

import (
	"context"
	"net/http"

	"decentralized-api/apiconfig"
	"decentralized-api/broker"

	"github.com/labstack/echo/v4"
)

func (s *Server) getUpgradeStatus(c echo.Context) error {
	plan := s.configManager.GetUpgradePlan()
	if plan.NodeVersion == "" {
		return c.JSON(http.StatusOK, map[string]string{"message": "No upgrade plan active"})
	}

	reports := s.nodeBroker.CheckVersionHealth(plan.NodeVersion)
	return c.JSON(http.StatusOK, reports)
}

type versionStatusRequest struct {
	Version string `json:"version"`
}

func (s *Server) postVersionStatus(c echo.Context) error {
	var req versionStatusRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid request body")
	}

	if req.Version == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Version field is required")
	}

	reports := s.nodeBroker.CheckVersionHealth(req.Version)

	// Auto-test trigger on version status check when timing allows
	getCmd := broker.NewGetNodesCommand()
	if err := s.nodeBroker.QueueMessage(getCmd); err == nil {
		responses := <-getCmd.Response
		for _, resp := range responses {
			var secs int64
			if resp.State.Timing != nil {
				secs = resp.State.Timing.SecondsUntilNextPoC
			}
			if s.tester.ShouldAutoTest(secs) {
				// Convert broker.Node to apiconfig.InferenceNodeConfig
				nodeCfg := apiconfig.InferenceNodeConfig{
					Id:               resp.Node.Id,
					Host:             resp.Node.Host,
					InferencePort:    resp.Node.InferencePort,
					InferenceSegment: resp.Node.InferenceSegment,
					PoCPort:          resp.Node.PoCPort,
					PoCSegment:       resp.Node.PoCSegment,
					MaxConcurrent:    resp.Node.MaxConcurrent,
					Hardware:         resp.Node.Hardware,
					Models:           make(map[string]apiconfig.ModelConfig),
				}
				for mid, margs := range resp.Node.Models {
					nodeCfg.Models[mid] = apiconfig.ModelConfig{Args: margs.Args}
				}

				result := s.tester.RunNodeTest(context.Background(), nodeCfg)
				if result != nil {
					if result.Status == TestFailed {
						cmd := broker.NewSetNodeFailureReasonCommand(resp.Node.Id, result.Error)
						_ = s.nodeBroker.QueueMessage(cmd)
					} else {
						cmd := broker.NewSetNodeFailureReasonCommand(resp.Node.Id, "")
						_ = s.nodeBroker.QueueMessage(cmd)
					}
					s.latestTestResults[resp.Node.Id] = result
				}
			}
		}
	}

	return c.JSON(http.StatusOK, reports)
}
