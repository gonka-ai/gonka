package admin

import (
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

	// Add the block to the shared queue. AddBlock processes the block synchronously
	// (and any contiguous successors), so an error here means a Cosmos submission
	// failed. Return HTTP 500 so Geth halts its downloader loop and retries the
	// failed block range (fail-closed).
	blockNumber, err := s.blockQueue.AddBlock(blockData)
	if err != nil {
		slog.Error("Failed to process bridge block", "blockNumber", blockData.BlockNumber, "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	// Return success response
	return c.JSON(http.StatusOK, map[string]interface{}{
		"status":        "success",
		"message":       "Block processed",
		"blockNumber":   blockNumber,
		"receiptsCount": len(blockData.Receipts),
		"queueSize":     len(s.blockQueue.GetPendingBlocks()),
	})
}
