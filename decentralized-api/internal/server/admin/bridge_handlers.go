package admin

import (
	"io"
	"log/slog"
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

	// Add the block to the shared queue
	blockNumber := s.blockQueue.AddBlock(blockData)

	// Return success response
	return c.JSON(200, map[string]interface{}{
		"status":        "success",
		"message":       "Block queued for processing",
		"blockNumber":   blockNumber,
		"receiptsCount": len(blockData.Receipts),
		"queueSize":     len(s.blockQueue.GetPendingBlocks()),
	})
}
