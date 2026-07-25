package public

import (
	"common/logging"
	"net/http"
	"sync"
	"time"

	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	"github.com/cosmos/cosmos-sdk/version"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
)

const versionsCacheTTL = 5 * time.Minute

type mlnodeVersionResponse struct {
	NodeID                 string `json:"node_id"`
	Version                string `json:"version"`
	PoCValidationInference bool   `json:"poc_validation_inference"`
}

type versionsResponse struct {
	Timestamp   string                  `json:"timestamp"`
	APIVersion  map[string]string       `json:"api_version"`
	NodeVersion map[string]string       `json:"node_version"`
	MLNodes     []mlnodeVersionResponse `json:"mlnodes"`
}

type versionsCache struct {
	mu        sync.RWMutex
	response  *versionsResponse
	expiresAt time.Time
	cacheTTL  time.Duration
}

func newVersionsCache() *versionsCache {
	return &versionsCache{
		cacheTTL: versionsCacheTTL,
	}
}

func (c *versionsCache) get() (*versionsResponse, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.response == nil || time.Now().After(c.expiresAt) {
		return nil, false
	}

	return c.response, true
}

func (s *Server) getVersions(ctx echo.Context) error {
	if cached, valid := s.versionsCache.get(); valid {
		return ctx.JSON(http.StatusOK, cached)
	}

	response, err := s.getOrGenerateVersionsResponse()
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to get node info",
		})
	}

	return ctx.JSON(http.StatusOK, response)
}

func (s *Server) getOrGenerateVersionsResponse() (*versionsResponse, error) {
	s.versionsCache.mu.Lock()
	defer s.versionsCache.mu.Unlock()

	if s.versionsCache.response != nil && time.Now().Before(s.versionsCache.expiresAt) {
		return s.versionsCache.response, nil
	}

	response, err := s.generateVersionsResponse()
	if err != nil {
		return nil, err
	}

	s.versionsCache.response = response
	s.versionsCache.expiresAt = time.Now().Add(s.versionsCache.cacheTTL)

	return response, nil
}

func (s *Server) generateVersionsResponse() (*versionsResponse, error) {
	cometClient := s.recorder.NewCometQueryClient()
	resp, err := cometClient.GetNodeInfo(s.recorder.GetContext(), &cmtservice.GetNodeInfoRequest{})
	if err != nil {
		logging.Error("Failed to get node info from cosmos node", types.Server, "error", err)
		return nil, err
	}
	nodeVersion := resp.ApplicationVersion

	mlnodes := make([]mlnodeVersionResponse, 0)
	if s.nodeBroker != nil {
		if nodes, nerr := s.nodeBroker.GetNodes(); nerr != nil {
			logging.Error("Failed to list ML nodes for /v1/versions", types.Server, "error", nerr)
		} else {
			for _, nodeResp := range nodes {
				mlnodes = append(mlnodes, mlnodeVersionResponse{
					NodeID:                 nodeResp.Node.Id,
					Version:                nodeResp.State.MlNodeVersion,
					PoCValidationInference: nodeResp.State.PoCValidationInference,
				})
			}
		}
	}

	return &versionsResponse{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		APIVersion: map[string]string{
			"application_name": version.AppName,
			"version":          version.Version,
			"commit":           version.Commit,
		},
		NodeVersion: map[string]string{
			"application_name": nodeVersion.Name,
			"version":          nodeVersion.Version,
			"commit":           nodeVersion.GitCommit,
		},
		MLNodes: mlnodes,
	}, nil
}
