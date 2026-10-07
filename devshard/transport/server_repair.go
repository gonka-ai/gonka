package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	json "github.com/goccy/go-json"
	"github.com/labstack/echo/v4"

	"devshard/heightsync"
	"devshard/logging"
	"devshard/observability"
)

// DefaultRepairTimeout is the unicast probe budget when the caller has no deadline.
const DefaultRepairTimeout = 3 * time.Second

// HandleHeightSyncRepair is POST /sessions/:id/heightsync/repair (group members only).
// Always answers HEIGHT when reachable; UNREACHABLE is requester-local.
func (s *Server) HandleHeightSyncRepair(c echo.Context) (err error) {
	op, finish := startHandlerSpan(c, "heightsync_repair")
	defer finish(&err)

	sender, err := getSender(c)
	if err != nil {
		return err
	}
	if !s.isGroupMember(sender) {
		return echo.NewHTTPError(http.StatusForbidden, "repair restricted to group members")
	}
	observability.Request.SetSender(op, sender)
	sessionID := c.Param("id")
	if sessionID != s.host.EscrowID() {
		return echo.NewHTTPError(http.StatusNotFound, "session not found")
	}

	body, err := getBody(c)
	if err != nil {
		return err
	}
	var req heightsync.RepairRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid json")
	}

	resp, err := s.ServeHeightSyncRepair(c.Request().Context(), sender, &req)
	if err != nil {
		return mapRepairHTTP(err)
	}
	return writeJSON(c, http.StatusOK, resp)
}

// ServeHeightSyncRepair is the transport-neutral core behind POST .../heightsync/repair
// and SessionService.RepairHeightSync. Callers enforce group membership.
func (s *Server) ServeHeightSyncRepair(ctx context.Context, sender string, req *heightsync.RepairRequest) (*heightsync.RepairResponse, error) {
	group := s.host.Group()
	if int(req.RequesterSlot) >= len(group) {
		return nil, ErrInvalidRequesterSlot
	}
	slotKey := group[req.RequesterSlot].ValidatorAddress
	if err := heightsync.VerifyRepairRequest(s.verifier, req, slotKey); err != nil {
		return nil, err
	}
	if !s.senderOwnsSlot(sender, req.RequesterSlot) {
		return nil, ErrRequesterSlotMismatch
	}

	resp, err := s.host.BuildRepairHeightResponse(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func mapRepairHTTP(err error) error {
	switch {
	case errors.Is(err, ErrInvalidRequesterSlot):
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	case errors.Is(err, heightsync.ErrRepairUnknownTurn):
		return echo.NewHTTPError(http.StatusNotFound, "unknown turn")
	case errors.Is(err, heightsync.ErrRepairResponderBudget):
		return echo.NewHTTPError(http.StatusTooManyRequests, "repair budget exhausted")
	case errors.Is(err, ErrRequesterSlotMismatch),
		errors.Is(err, heightsync.ErrRepairVerify),
		errors.Is(err, heightsync.ErrRepairNoSig),
		errors.Is(err, heightsync.ErrRepairEmpty):
		return echo.NewHTTPError(http.StatusForbidden, err.Error())
	default:
		logging.Debug("repair response failed", "subsystem", "heightsync", "error", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "repair response failed")
	}
}

func (s *Server) senderOwnsSlot(sender string, slot uint32) bool {
	group := s.host.Group()
	if int(slot) >= len(group) {
		return false
	}
	if sender == group[slot].ValidatorAddress {
		return true
	}
	return s.host.IsWarmKeyForSlot(sender, slot)
}

// RepairProbe unicasts a signed repair request to targetSlot. Timeout /
// unsigned / bad signature become an error; the host maps that to UNREACHABLE.
// CloneWithSigner re-signs with this host key. RPC clients in peerClients
// must already be attached as s.host.Signer() (SetPeerClients); a
// user-signed RPCClient clone fails openSigned.
func (s *Server) RepairProbe(ctx context.Context, targetSlot uint32, req *heightsync.RepairRequest) (*heightsync.RepairResponse, error) {
	if s.peerClients == nil {
		return nil, fmt.Errorf("repair: no peer clients")
	}
	pc, ok := s.peerClients[int(targetSlot)]
	if !ok || pc == nil {
		return nil, fmt.Errorf("repair: no client for slot %d", targetSlot)
	}
	timeout := DefaultRepairTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remain := time.Until(deadline); remain > 0 && remain < timeout {
			timeout = remain
		}
	}
	client := pc.CloneWithSigner(s.host.Signer(), timeout)
	resp, err := client.HeightSyncRepair(ctx, req)
	if err != nil {
		return nil, err
	}
	group := s.host.Group()
	if int(targetSlot) >= len(group) {
		return nil, fmt.Errorf("repair: invalid target slot")
	}
	if err := heightsync.VerifyRepairResponse(s.verifier, resp, group[targetSlot].ValidatorAddress); err != nil {
		return nil, err
	}
	resp.Outcome = heightsync.RepairOutcomeHeight
	return resp, nil
}
