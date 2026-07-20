// Package adminface exposes POST /testenv/* routes that mutate mock-chain store
// for citest fault injection. mock-dapi proxies these when running as a separate
// process; tests mount the same handlers in-process.
package adminface

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"devshard/testenv/mockchain/store"

	"github.com/labstack/echo/v4"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// ParamsRequest patches devshard escrow params on the mock-chain store.
type ParamsRequest struct {
	MaxNonce            *uint32 `json:"max_nonce,omitempty"`
	RefusalTimeout      *int64  `json:"refusal_timeout,omitempty"`
	ExecutionTimeout    *int64  `json:"execution_timeout,omitempty"`
	ValidationRate      *uint32 `json:"validation_rate,omitempty"`
	VoteThresholdFactor *uint32 `json:"vote_threshold_factor,omitempty"`
}

// EpochRequest sets or advances epoch metadata.
type EpochRequest struct {
	Index               *uint64 `json:"index,omitempty"`
	PocStartBlockHeight *int64  `json:"poc_start_block_height,omitempty"`
	Advance             bool    `json:"advance,omitempty"`
}

// Mount registers POST /testenv/params, POST /testenv/epoch, POST /testenv/escrow,
// POST /testenv/grantees, and GET /testenv/revision on g.
func Mount(g *echo.Group, st *store.Store, advancer EpochAdvancer, escrowPub EscrowPublisher) {
	if g == nil || st == nil {
		panic("adminface: nil group or store")
	}
	g.POST("/testenv/params", handleParams(st))
	g.POST("/testenv/epoch", handleEpoch(st, advancer))
	g.POST("/testenv/escrow", handleEscrow(st, escrowPub))
	g.POST("/testenv/grantees", handleGrantees(st))
	g.GET("/testenv/revision", handleRevision(st))
}

func handleParams(st *store.Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		var req ParamsRequest
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		st.PatchDevshardEscrowParams(func(p *inferencetypes.DevshardEscrowParams) {
			if req.MaxNonce != nil {
				p.MaxNonce = *req.MaxNonce
			}
			if req.RefusalTimeout != nil {
				p.RefusalTimeout = *req.RefusalTimeout
			}
			if req.ExecutionTimeout != nil {
				p.ExecutionTimeout = *req.ExecutionTimeout
			}
			if req.ValidationRate != nil {
				p.ValidationRate = *req.ValidationRate
			}
			if req.VoteThresholdFactor != nil {
				p.VoteThresholdFactor = *req.VoteThresholdFactor
			}
		})
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	}
}

// RevisionResponse reports mock-chain block and params revision heights.
type RevisionResponse struct {
	BlockHeight             int64 `json:"block_height"`
	ParamsBlockHeight       int64 `json:"params_block_height"`
	NextPocStartBlockHeight int64 `json:"next_poc_start_block_height"`
	EpochIndex              uint64 `json:"epoch_index"`
}

func handleEpoch(st *store.Store, advancer EpochAdvancer) echo.HandlerFunc {
	return func(c echo.Context) error {
		var req EpochRequest
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if req.Advance {
			if advancer != nil {
				resp, err := advancer.AdvanceEpoch(c.Request().Context())
				if err != nil {
					return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
				}
				return c.JSON(http.StatusOK, resp)
			}
			epoch := st.AdvanceEpochWithoutCatchUp()
			return c.JSON(http.StatusOK, EpochAdvanceResponse{
				Epoch:                   epoch,
				ToBlockHeight:           st.GetBlockHeight(),
				NextPocStartBlockHeight: st.GetNextPocStartBlockHeight(),
			})
		}
		epoch := st.GetEpoch()
		if req.Index != nil {
			epoch.Index = *req.Index
		}
		if req.PocStartBlockHeight != nil {
			epoch.PocStartBlockHeight = *req.PocStartBlockHeight
		}
		st.SetEpoch(epoch)
		return c.JSON(http.StatusOK, st.GetEpoch())
	}
}

func handleRevision(st *store.Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusOK, RevisionResponse{
			BlockHeight:             st.GetBlockHeight(),
			ParamsBlockHeight:       st.GetParamsBlockHeight(),
			NextPocStartBlockHeight: st.GetNextPocStartBlockHeight(),
			EpochIndex:              st.GetEpoch().Index,
		})
	}
}

func handleEscrow(st *store.Store, pub EscrowPublisher) echo.HandlerFunc {
	return func(c echo.Context) error {
		var req EscrowRequest
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if !req.Settle {
			return echo.NewHTTPError(http.StatusBadRequest, "only settle=true is supported")
		}
		id := uint64(1)
		if req.ID != nil {
			id = *req.ID
		}
		if !st.MarkEscrowSettled(id) {
			return echo.NewHTTPError(http.StatusNotFound, "escrow not found")
		}
		if pub != nil {
			if err := pub.PublishEscrowSettled(id, "testenv-settler", 0, 0, 0); err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
			}
		}
		return c.JSON(http.StatusOK, map[string]any{"status": "settled", "id": id})
	}
}

// GranteesRequest replaces warm-key grantees for a validator granter.
type GranteesRequest struct {
	GranterAddress string   `json:"granter_address"`
	MessageTypeURL string   `json:"message_type_url,omitempty"`
	Grantees       []string `json:"grantees"`
}

func handleGrantees(st *store.Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		var req GranteesRequest
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if strings.TrimSpace(req.GranterAddress) == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "granter_address is required")
		}
		msgType := req.MessageTypeURL
		if msgType == "" {
			msgType = "/inference.inference.MsgStartInference"
		}
		out := make([]inferencetypes.Grantee, 0, len(req.Grantees))
		for _, addr := range req.Grantees {
			addr = strings.TrimSpace(addr)
			if addr == "" {
				continue
			}
			out = append(out, inferencetypes.Grantee{Address: addr})
		}
		st.SetGrantees(req.GranterAddress, msgType, out)
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	}
}

// Client forwards fault-injection requests to a remote mock-chain admin server.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient creates a client for mock-chain adminface (e.g. http://mock-chain:9191).
func NewClient(baseURL string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: http.DefaultClient}
}

// BaseURL returns the configured admin base URL.
func (c *Client) BaseURL() string {
	if c == nil {
		return ""
	}
	return c.baseURL
}

func (c *Client) PatchParams(ctx context.Context, req ParamsRequest) error {
	if c == nil || c.baseURL == "" {
		return errors.New("adminface: client not configured")
	}
	return c.postJSON(ctx, "/testenv/params", req)
}

func (c *Client) PatchEpoch(ctx context.Context, req EpochRequest) error {
	if c == nil || c.baseURL == "" {
		return errors.New("adminface: client not configured")
	}
	return c.postJSON(ctx, "/testenv/epoch", req)
}

func (c *Client) PatchEscrow(ctx context.Context, req EscrowRequest) error {
	if c == nil || c.baseURL == "" {
		return errors.New("adminface: client not configured")
	}
	return c.postJSON(ctx, "/testenv/escrow", req)
}

func (c *Client) PatchGrantees(ctx context.Context, req GranteesRequest) error {
	if c == nil || c.baseURL == "" {
		return errors.New("adminface: client not configured")
	}
	return c.postJSON(ctx, "/testenv/grantees", req)
}

// GetRevision reads block_height and params_block_height from mock-chain admin.
func (c *Client) GetRevision(ctx context.Context) (RevisionResponse, error) {
	if c == nil || c.baseURL == "" {
		return RevisionResponse{}, errors.New("adminface: client not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/testenv/revision", nil)
	if err != nil {
		return RevisionResponse{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return RevisionResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return RevisionResponse{}, fmt.Errorf("adminface GET /testenv/revision: %s: %s", resp.Status, string(b))
	}
	var out RevisionResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return RevisionResponse{}, err
	}
	return out, nil
}
