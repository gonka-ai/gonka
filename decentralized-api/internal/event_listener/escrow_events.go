package event_listener

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"common/logging"
	"decentralized-api/apiconfig"
	"decentralized-api/internal/event_listener/chainevents"

	"github.com/productscience/inference/x/inference/types"
)

const (
	escrowCreatedEvent = "devshard_escrow_created"
	escrowSettledEvent = "devshard_escrow_settled"
)

// ErrEscrowNotFound is returned by escrowQuerier implementations when the
// escrow does not exist on chain.
var ErrEscrowNotFound = errors.New("event_listener: escrow not found")

// EscrowSlotInfo is the minimal escrow membership info needed for host-event
// slot filtering. It is intentionally decoupled from devshard's richer
// bridge.EscrowInfo since decentralized-api does not depend on the devshard
// module (devshard depends on common/decentralized-api's chain queries, not
// the other way around).
type EscrowSlotInfo struct {
	EscrowID string
	Slots    []string // host addresses, len == DevshardGroupSize
}

// escrowQuerier resolves escrow slot membership for host-event filtering.
type escrowQuerier interface {
	GetEscrow(ctx context.Context, escrowID string) (*EscrowSlotInfo, error)
}

type DevshardEscrowCreatedEventHandler struct{}

func (e *DevshardEscrowCreatedEventHandler) GetName() string { return "devshard_escrow_created" }

func (e *DevshardEscrowCreatedEventHandler) CanHandle(event *chainevents.JSONRPCResponse) bool {
	return len(event.Result.Events[escrowCreatedEvent+".escrow_id"]) > 0
}

func (e *DevshardEscrowCreatedEventHandler) Handle(event *chainevents.JSONRPCResponse, el *EventListener) error {
	return nil
}

type DevshardEscrowSettledEventHandler struct{}

func (e *DevshardEscrowSettledEventHandler) GetName() string { return "devshard_escrow_settled" }

func (e *DevshardEscrowSettledEventHandler) CanHandle(event *chainevents.JSONRPCResponse) bool {
	return len(event.Result.Events[escrowSettledEvent+".escrow_id"]) > 0
}

func (e *DevshardEscrowSettledEventHandler) Handle(event *chainevents.JSONRPCResponse, el *EventListener) error {
	return nil
}

// parseEscrowCreated reads one escrow-created attribute set. ok is false when
// the map has no escrow id.
func parseEscrowCreated(ev map[string][]string) (apiconfig.HostEvent, bool, error) {
	raw := firstAttr(ev, escrowCreatedEvent+".escrow_id")
	if raw == "" {
		return apiconfig.HostEvent{}, false, nil
	}
	escrowID, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return apiconfig.HostEvent{}, false, fmt.Errorf("parse escrow_id: %w", err)
	}
	epochIndex, err := strconv.ParseUint(firstAttr(ev, escrowCreatedEvent+".epoch_index"), 10, 64)
	if err != nil {
		logging.Warn("host_events: malformed epoch_index on escrow_created; defaulting to 0", types.EventProcessing,
			"escrow_id", escrowID, "raw", firstAttr(ev, escrowCreatedEvent+".epoch_index"), "error", err)
		epochIndex = 0
	}
	return apiconfig.HostEvent{
		Kind: apiconfig.HostEventKindEscrowCreated,
		Escrow: &apiconfig.EscrowPayload{
			EscrowID:   escrowID,
			EpochIndex: epochIndex,
			ModelID:    firstAttr(ev, escrowCreatedEvent+".model_id"),
			Creator:    firstAttr(ev, escrowCreatedEvent+".creator"),
			Amount:     firstAttr(ev, escrowCreatedEvent+".amount"),
		},
	}, true, nil
}

// parseEscrowSettled reads one escrow-settled attribute set.
func parseEscrowSettled(ev map[string][]string) (apiconfig.HostEvent, bool, error) {
	raw := firstAttr(ev, escrowSettledEvent+".escrow_id")
	if raw == "" {
		return apiconfig.HostEvent{}, false, nil
	}
	escrowID, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return apiconfig.HostEvent{}, false, fmt.Errorf("parse escrow_id: %w", err)
	}
	return apiconfig.HostEvent{
		Kind: apiconfig.HostEventKindEscrowSettled,
		Escrow: &apiconfig.EscrowPayload{
			EscrowID:    escrowID,
			Settler:     firstAttr(ev, escrowSettledEvent+".settler"),
			TotalPayout: firstAttr(ev, escrowSettledEvent+".total_payout"),
			Fees:        firstAttr(ev, escrowSettledEvent+".fees"),
			Remainder:   firstAttr(ev, escrowSettledEvent+".remainder"),
		},
	}, true, nil
}

// escrowMembership returns (holds, resolved). resolved=false means the query
// failed or no querier is set; callers append the event anyway.
func escrowMembership(ctx context.Context, q escrowQuerier, addr, escrowID string) (holds bool, resolved bool) {
	if q == nil {
		return false, false
	}
	info, err := q.GetEscrow(ctx, escrowID)
	if err != nil || info == nil {
		return false, false
	}
	if addr == "" {
		return false, false
	}
	for _, slot := range info.Slots {
		if strings.EqualFold(slot, addr) {
			return true, true
		}
	}
	return false, true
}

func (el *EventListener) localParticipantAddress() string {
	if el.participantAddress != "" {
		return el.participantAddress
	}
	if el.nodeBroker != nil {
		if addr := el.nodeBroker.GetParticipantAddress(); addr != "" {
			return addr
		}
	}
	return el.transactionRecorder.GetAddress()
}

func firstAttr(events map[string][]string, key string) string {
	if vals := events[key]; len(vals) > 0 {
		return vals[0]
	}
	return ""
}
