package session

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"devshard/bridge"
	"devshard/state"
	"devshard/storage"
	"devshard/transport"
	"devshard/types"
)

// sessionForParticipant returns a live session when addr is allowed on it.
// It never CreateSession: only the escrow creator (BindOwnerChat) or a
// participant that presents a gateway-signed start proof may bind a version.
func (m *HostManager) sessionForParticipant(escrowID, addr string) (*transport.Server, error) {
	srv, err := m.SessionServerExisting(escrowID)
	if err != nil {
		if errors.Is(err, storage.ErrSessionNotFound) {
			return nil, err
		}
		return escrowNotOpen(escrowID, err)
	}
	if srv == nil {
		return nil, storage.ErrSessionNotFound
	}
	if !srv.AllowsSender(addr) {
		return nil, nil
	}
	return srv, nil
}

func (m *HostManager) sessionForStartProof(escrowID, addr string, diffs []types.Diff, claimedVersion string) (*transport.Server, error) {
	srv, err := m.sessionForParticipant(escrowID, addr)
	if err == nil {
		return srv, nil
	}
	if !errors.Is(err, storage.ErrSessionNotFound) {
		return nil, err
	}
	if m.bridge == nil {
		return escrowNotOpen(escrowID, err)
	}
	escrow := m.warmedEscrow(escrowID)
	var gerr error
	if escrow == nil {
		escrow, gerr = m.fetchEscrowForBind(escrowID, addr)
	}
	if gerr != nil {
		return nil, fmt.Errorf("get escrow: %w", gerr)
	}
	if escrow == nil {
		return escrowNotOpen(escrowID, err)
	}
	if escrow.Settled {
		m.rememberResolutionFailure(escrowID, bridge.ErrEscrowSettled, time.Now())
		return nil, fmt.Errorf("%w: escrow %s", bridge.ErrEscrowSettled, escrowID)
	}
	if !escrowLookupEligible(escrow, nil, addr) {
		return nil, nil
	}
	version, err := state.GatewayStartVersion(m.verifier, escrow.CreatorAddress, escrowID, diffs)
	if err != nil {
		return nil, err
	}
	claimed := strings.TrimSpace(claimedVersion)
	if claimed != "" && claimed != version {
		return nil, fmt.Errorf("%w: request %s start %s", types.ErrProtocolVersionMismatch, claimed, version)
	}
	if version != m.boundVersion {
		return nil, fmt.Errorf("%w: proof %s, host %s", storage.ErrSessionVersionConflict, version, m.boundVersion)
	}
	srv, cerr := m.getOrCreate(escrowID, escrow)
	if cerr != nil {
		return nil, cerr
	}
	if srv == nil {
		return nil, storage.ErrSessionNotFound
	}
	if !srv.AllowsSender(addr) {
		return nil, nil
	}
	return srv, nil
}

func (m *HostManager) bindGroupPeerFromBody(escrowID, addr string, body []byte) (*transport.Server, error) {
	srv, err := m.sessionForParticipant(escrowID, addr)
	if err == nil {
		return srv, nil
	}
	if !errors.Is(err, storage.ErrSessionNotFound) {
		return nil, err
	}
	diffs, claimed, peekErr := transport.PeekStartProof(body)
	if peekErr != nil {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "invalid json")
	}
	if len(diffs) == 0 {
		if m.bridge == nil {
			return escrowNotOpen(escrowID, err)
		}
		escrow := m.warmedEscrow(escrowID)
		var gerr error
		if escrow == nil {
			escrow, gerr = m.fetchEscrowForBind(escrowID, addr)
		}
		if gerr != nil {
			return nil, fmt.Errorf("get escrow: %w", gerr)
		}
		if escrow == nil || !escrowLookupEligible(escrow, nil, addr) {
			return nil, nil
		}
		return nil, types.ErrStartProofMissing
	}
	return m.sessionForStartProof(escrowID, addr, diffs, claimed)
}
