package admin

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
)

// StorePayloadRequest is the request body for storing payloads
type StorePayloadRequest struct {
	PromptPayload   string `json:"prompt_payload"`
	ResponsePayload string `json:"response_payload"`
	EpochId         string `json:"epoch_id"`
}

// StorePayloadResponse is the response for storing payloads
type StorePayloadResponse struct {
	Status      string `json:"status"`
	InferenceId string `json:"inference_id"`
	EpochId     uint64 `json:"epoch_id"`
}

// storePayload handles POST requests to store payloads directly to PayloadStorage.
// This endpoint is used by testermint to store payloads when using InferenceTestHelper
// which bypasses the normal REST API flow.
func (s *Server) storePayload(c echo.Context) error {
	inferenceId := c.QueryParam("inference_id")
	if inferenceId == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "inference_id required"})
	}

	var req StorePayloadRequest
	if err := c.Bind(&req); err != nil {
		slog.Error("Failed to bind request", "error", err)
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body: " + err.Error()})
	}

	if req.EpochId == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "epoch_id required"})
	}

	// Parse epoch_id string to uint64
	epochId, err := strconv.ParseUint(req.EpochId, 10, 64)
	if err != nil {
		slog.Error("Failed to parse epoch_id", "epochId", req.EpochId, "error", err)
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid epoch_id: " + err.Error()})
	}

	if s.payloadStorage == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "payload storage not configured"})
	}

	// Store payloads
	if err := s.payloadStorage.Store(c.Request().Context(), inferenceId, epochId, []byte(req.PromptPayload), []byte(req.ResponsePayload)); err != nil {
		slog.Error("Failed to store payload", "inferenceId", inferenceId, "epochId", epochId, "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to store payload: " + err.Error()})
	}

	slog.Info("Stored payload via admin endpoint", "inferenceId", inferenceId, "epochId", epochId)

	return c.JSON(http.StatusOK, StorePayloadResponse{
		Status:      "success",
		InferenceId: inferenceId,
		EpochId:     epochId,
	})
}
