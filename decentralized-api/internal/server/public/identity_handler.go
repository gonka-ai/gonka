package public

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

const identityCacheTTL = 5 * time.Minute

type IdentityData struct {
	Address   string `json:"address"`
	Block     int64  `json:"block"`
	Timestamp string `json:"timestamp"`
}

type IdentityResponse struct {
	Data      IdentityData `json:"data"`
	Signature string       `json:"signature"`
}

type identityCache struct {
	mu        sync.RWMutex
	response  *IdentityResponse
	expiresAt time.Time
	cacheTTL  time.Duration
}

func newIdentityCache() *identityCache {
	return &identityCache{
		cacheTTL: identityCacheTTL,
	}
}

func (c *identityCache) get() (*IdentityResponse, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.response == nil || time.Now().After(c.expiresAt) {
		return nil, false
	}

	return c.response, true
}

func (c *identityCache) set(response *IdentityResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.response = response
	c.expiresAt = time.Now().Add(c.cacheTTL)
}

func (s *Server) getIdentity(ctx echo.Context) error {
	if cached, valid := s.identityCache.get(); valid {
		return ctx.JSON(http.StatusOK, cached)
	}

	response, err := s.generateIdentityResponse()
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to generate identity: %v", err),
		})
	}

	s.identityCache.set(response)

	return ctx.JSON(http.StatusOK, response)
}

func (s *Server) generateIdentityResponse() (*IdentityResponse, error) {
	address := s.recorder.GetAccountAddress()
	if address == "" {
		return nil, fmt.Errorf("failed to get account address")
	}

	status, err := s.recorder.Status(s.recorder.GetContext())
	if err != nil {
		return nil, fmt.Errorf("failed to get status: %w", err)
	}
	block := status.SyncInfo.LatestBlockHeight

	timestamp := time.Now().UTC().Format(time.RFC3339)

	data := IdentityData{
		Address:   address,
		Block:     block,
		Timestamp: timestamp,
	}

	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal data: %w", err)
	}

	signatureBytes, err := s.recorder.SignBytes(jsonBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to sign data: %w", err)
	}

	signature := base64.StdEncoding.EncodeToString(signatureBytes)

	return &IdentityResponse{
		Data:      data,
		Signature: signature,
	}, nil
}
