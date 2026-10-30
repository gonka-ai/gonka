package mockdapi

import (
	"context"
	"net/http"

	"common/nodemanager/gen"
	cosrv "devshard/chainoracle/server"
	"devshard/testenv/mockchain/adminface"

	"github.com/labstack/echo/v4"
)

// EscrowCreatedRequest triggers a mock host-events escrow-created event so the
// devshardd GetHostEvents long-poll warm can be exercised in citest.
type EscrowCreatedRequest struct {
	EscrowID   uint64 `json:"escrow_id"`
	EpochIndex uint64 `json:"epoch_index,omitempty"`
	ModelID    string `json:"model_id,omitempty"`
	Creator    string `json:"creator,omitempty"`
	Amount     string `json:"amount,omitempty"`
}

// StallRequest is POST /testenv/host-events/stall.
type StallRequest struct {
	Stalled bool `json:"stalled"`
}

// WarmKeyRequest is POST /testenv/host-events/warm-key.
type WarmKeyRequest struct {
	Granter       string `json:"granter"`
	Grantee       string `json:"grantee"`
	GrantID       uint64 `json:"grant_id"`
	Height        int64  `json:"height"`
	ExpiresAtUnix int64  `json:"expires_at_unix"`
	End           string `json:"end"`
}

// BlockAppendRequest is POST /testenv/host-events/block. Hash and Header are
// taken as given, including a header that does not hash to hash.
type BlockAppendRequest struct {
	Height           int64  `json:"height"`
	Hash             []byte `json:"hash"`
	HeaderTimeUnixMs int64  `json:"header_time_unix_ms"`
	EpochIndex       uint64 `json:"epoch_index"`
	Header           []byte `json:"header"`
}

func mountTestenvProxy(g *echo.Group, admin *adminface.Client, refresh func(context.Context) error, versions *versionStore, hostEvents *hostEventRing) {
	g.POST("/testenv/params", func(c echo.Context) error {
		if admin == nil || admin.BaseURL() == "" {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "mock-chain testenv admin not configured")
		}
		var req adminface.ParamsRequest
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if err := admin.PatchParams(c.Request().Context(), req); err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, err.Error())
		}
		if refresh != nil {
			if err := refresh(c.Request().Context()); err != nil {
				return echo.NewHTTPError(http.StatusBadGateway, err.Error())
			}
		}
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
	g.POST("/testenv/epoch", func(c echo.Context) error {
		if admin == nil || admin.BaseURL() == "" {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "mock-chain testenv admin not configured")
		}
		var req adminface.EpochRequest
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if err := admin.PatchEpoch(c.Request().Context(), req); err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, err.Error())
		}
		if refresh != nil {
			if err := refresh(c.Request().Context()); err != nil {
				return echo.NewHTTPError(http.StatusBadGateway, err.Error())
			}
		}
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
	g.POST("/testenv/escrow", func(c echo.Context) error {
		if admin == nil || admin.BaseURL() == "" {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "mock-chain testenv admin not configured")
		}
		var req adminface.EscrowRequest
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if err := admin.PatchEscrow(c.Request().Context(), req); err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, err.Error())
		}
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
	g.POST("/testenv/grantees", func(c echo.Context) error {
		if admin == nil || admin.BaseURL() == "" {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "mock-chain testenv admin not configured")
		}
		var req adminface.GranteesRequest
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if err := admin.PatchGrantees(c.Request().Context(), req); err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, err.Error())
		}
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
	g.GET("/testenv/versions", func(c echo.Context) error {
		if versions == nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "mock-dapi versions not configured")
		}
		current, err := versions.Versions(c.Request().Context())
		if err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, err.Error())
		}
		return c.JSON(http.StatusOK, cosrv.VersionConfig{Versions: current})
	})
	g.POST("/testenv/versions", func(c echo.Context) error {
		if versions == nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "mock-dapi versions not configured")
		}
		var req cosrv.VersionConfig
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		versions.Set(req.Versions)
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
	g.POST("/testenv/host-events/escrow-created", func(c echo.Context) error {
		if hostEvents == nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "mock-dapi host events not configured")
		}
		var req EscrowCreatedRequest
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if req.EscrowID == 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "escrow_id is required")
		}
		seq := hostEvents.AppendEscrowCreated(&gen.EscrowPayload{
			EscrowId:   req.EscrowID,
			EpochIndex: req.EpochIndex,
			ModelId:    req.ModelID,
			Creator:    req.Creator,
			Amount:     req.Amount,
		})
		return c.JSON(http.StatusOK, map[string]any{"status": "ok", "seq": seq})
	})
	g.POST("/testenv/host-events/stall", func(c echo.Context) error {
		if hostEvents == nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "mock-dapi host events not configured")
		}
		var req StallRequest
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		hostEvents.SetStalled(req.Stalled)
		return c.JSON(http.StatusOK, map[string]any{"status": "ok", "stalled": req.Stalled})
	})
	g.POST("/testenv/host-events/generation", func(c echo.Context) error {
		if hostEvents == nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "mock-dapi host events not configured")
		}
		genID := hostEvents.BumpGeneration()
		return c.JSON(http.StatusOK, map[string]any{"status": "ok", "generation": genID})
	})
	g.POST("/testenv/host-events/warm-key", func(c echo.Context) error {
		if hostEvents == nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "mock-dapi host events not configured")
		}
		var req WarmKeyRequest
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		seq := hostEvents.AppendWarmKey(&gen.WarmKeyPayload{
			Granter:       req.Granter,
			Grantee:       req.Grantee,
			GrantId:       req.GrantID,
			Height:        req.Height,
			ExpiresAtUnix: req.ExpiresAtUnix,
			End:           req.End,
		})
		return c.JSON(http.StatusOK, map[string]any{"status": "ok", "seq": seq})
	})
	g.POST("/testenv/host-events/block", func(c echo.Context) error {
		if hostEvents == nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "mock-dapi host events not configured")
		}
		var req BlockAppendRequest
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if req.Height <= 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "height is required")
		}
		seq := hostEvents.AppendBlock(&gen.BlockPayload{
			Height:           req.Height,
			Hash:             req.Hash,
			HeaderTimeUnixMs: req.HeaderTimeUnixMs,
			EpochIndex:       req.EpochIndex,
			Header:           req.Header,
		})
		return c.JSON(http.StatusOK, map[string]any{"status": "ok", "seq": seq})
	})
	g.POST("/testenv/escrow-query-fault", func(c echo.Context) error {
		if admin == nil || admin.BaseURL() == "" {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "mock-chain testenv admin not configured")
		}
		var req adminface.EscrowQueryFaultRequest
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if err := admin.PatchEscrowQueryFault(c.Request().Context(), req); err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, err.Error())
		}
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
}
