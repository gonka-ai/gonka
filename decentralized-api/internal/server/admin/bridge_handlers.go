package admin

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	pserver "decentralized-api/internal/server/public"

	"github.com/labstack/echo/v4"
)

// postBridgeBlock handles POST requests to submit finalized blocks with optional receipts
func (s *Server) postBridgeBlock(c echo.Context) error {
	// Debug: Log raw request body
	rawBody := c.Request().Body
	bodyBytes, err := io.ReadAll(rawBody)
	if err != nil {
		slog.Error("Failed to read request body", "error", err)
		return c.JSON(400, map[string]string{"error": "Failed to read request body"})
	}

	// Log the raw JSON for debugging
	slog.Info("Received raw request body", "body", string(bodyBytes))

	// Reset the body for binding
	c.Request().Body = io.NopCloser(strings.NewReader(string(bodyBytes)))

	var blockData pserver.BridgeBlock
	if err := c.Bind(&blockData); err != nil {
		slog.Error("Failed to decode block data", "error", err)
		return c.JSON(400, map[string]string{"error": "Invalid request body: " + err.Error()})
	}

	// Validate required fields
	if blockData.BlockNumber == "" || blockData.ReceiptsRoot == "" || blockData.OriginChain == "" {
		return c.JSON(400, map[string]string{"error": "Required fields missing: blockNumber, receiptsRoot, originChain"})
	}

	slog.Info("Received finalized block",
		"blockNumber", blockData.BlockNumber,
		"originChain", blockData.OriginChain,
		"receiptsRoot", blockData.ReceiptsRoot,
		"receiptsCount", len(blockData.Receipts))

	// The POST is a receipt-ACK, not a commit report. AddBlock returns an error
	// ONLY when the block could not be accepted into the queue:
	//   - ErrBridgeQueueFull -> 503 back-pressure (Geth's sendRangeDirectly stops
	//     and resends later; nothing is dropped),
	//   - any other error -> 400 (malformed / unexpected input).
	// Otherwise the block is received (buffered/duplicate/bootstrapping) -> 200.
	// Commit success/failure is owned by the drain and never reported here.
	blockNumber, err := s.blockQueue.AddBlock(blockData)
	switch {
	case err == nil:
		return c.JSON(http.StatusOK, map[string]interface{}{
			"status":        "received",
			"message":       "Block received",
			"blockNumber":   blockNumber,
			"receiptsCount": len(blockData.Receipts),
			"queueSize":     len(s.blockQueue.GetPendingBlocks()),
		})
	case errors.Is(err, pserver.ErrBridgeQueueFull):
		slog.Warn("Bridge: back-pressure, rejecting block", "blockNumber", blockData.BlockNumber, "error", err)
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
	default:
		slog.Error("Bridge: malformed/unexpected block", "blockNumber", blockData.BlockNumber, "error", err)
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
}
