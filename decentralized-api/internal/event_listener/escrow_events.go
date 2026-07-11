package event_listener

import (
	"fmt"
	"strconv"
	"strings"

	"decentralized-api/apiconfig"
	"decentralized-api/internal/event_listener/chainevents"
	"decentralized-api/logging"

	"devshard/bridge"

	"github.com/productscience/inference/x/inference/types"
)

const (
	escrowCreatedEvent = "devshard_escrow_created"
	escrowSettledEvent = "devshard_escrow_settled"
)

// escrowQuerier is the subset of bridge used for slot membership on create/settle.
type escrowQuerier interface {
	GetEscrow(escrowID string) (*bridge.EscrowInfo, error)
}

type DevshardEscrowCreatedEventHandler struct{}

func (e *DevshardEscrowCreatedEventHandler) GetName() string { return "devshard_escrow_created" }

func (e *DevshardEscrowCreatedEventHandler) CanHandle(event *chainevents.JSONRPCResponse) bool {
	return len(event.Result.Events[escrowCreatedEvent+".escrow_id"]) > 0
}

func (e *DevshardEscrowCreatedEventHandler) Handle(event *chainevents.JSONRPCResponse, el *EventListener) error {
	if el.hostEvents == nil {
		return nil
	}
	ev := event.Result.Events
	escrowID, err := strconv.ParseUint(firstAttr(ev, escrowCreatedEvent+".escrow_id"), 10, 64)
	if err != nil {
		return fmt.Errorf("parse escrow_id: %w", err)
	}
	epochIndex, err := strconv.ParseUint(firstAttr(ev, escrowCreatedEvent+".epoch_index"), 10, 64)
	if err != nil {
		logging.Warn("host_events: malformed epoch_index on escrow_created; defaulting to 0", types.EventProcessing,
			"escrow_id", escrowID, "raw", firstAttr(ev, escrowCreatedEvent+".epoch_index"), "error", err)
		epochIndex = 0
	}
	payload := &apiconfig.EscrowPayload{
		EscrowID:   escrowID,
		EpochIndex: epochIndex,
		ModelID:    firstAttr(ev, escrowCreatedEvent+".model_id"),
		Creator:    firstAttr(ev, escrowCreatedEvent+".creator"),
		Amount:     firstAttr(ev, escrowCreatedEvent+".amount"),
	}

	hold, ok := el.localHoldsEscrowSlot(strconv.FormatUint(escrowID, 10))
	if ok && !hold {
		logging.Debug("host_events: skip escrow_created; local node not in slots", types.EventProcessing,
			"escrow_id", escrowID)
		return nil
	}
	if !ok {
		logging.Warn("host_events: escrow membership unresolved; appending created anyway", types.EventProcessing,
			"escrow_id", escrowID)
	}

	el.hostEvents.Append(apiconfig.HostEvent{
		Kind:   apiconfig.HostEventKindEscrowCreated,
		Escrow: payload,
	})
	return nil
}

type DevshardEscrowSettledEventHandler struct{}

func (e *DevshardEscrowSettledEventHandler) GetName() string { return "devshard_escrow_settled" }

func (e *DevshardEscrowSettledEventHandler) CanHandle(event *chainevents.JSONRPCResponse) bool {
	return len(event.Result.Events[escrowSettledEvent+".escrow_id"]) > 0
}

func (e *DevshardEscrowSettledEventHandler) Handle(event *chainevents.JSONRPCResponse, el *EventListener) error {
	if el.hostEvents == nil {
		return nil
	}
	ev := event.Result.Events
	escrowID, err := strconv.ParseUint(firstAttr(ev, escrowSettledEvent+".escrow_id"), 10, 64)
	if err != nil {
		return fmt.Errorf("parse escrow_id: %w", err)
	}
	payload := &apiconfig.EscrowPayload{
		EscrowID:    escrowID,
		Settler:     firstAttr(ev, escrowSettledEvent+".settler"),
		TotalPayout: firstAttr(ev, escrowSettledEvent+".total_payout"),
		Fees:        firstAttr(ev, escrowSettledEvent+".fees"),
		Remainder:   firstAttr(ev, escrowSettledEvent+".remainder"),
	}

	hold, ok := el.localHoldsEscrowSlot(strconv.FormatUint(escrowID, 10))
	if ok && !hold {
		logging.Debug("host_events: skip escrow_settled; local node not in slots", types.EventProcessing,
			"escrow_id", escrowID)
		return nil
	}
	if !ok {
		logging.Warn("host_events: escrow membership unresolved; appending settled anyway", types.EventProcessing,
			"escrow_id", escrowID)
	}

	el.hostEvents.Append(apiconfig.HostEvent{
		Kind:   apiconfig.HostEventKindEscrowSettled,
		Escrow: payload,
	})
	return nil
}

// localHoldsEscrowSlot returns (holds, resolved).
// resolved=false means GetEscrow failed / querier unset — callers fall back to append.
func (el *EventListener) localHoldsEscrowSlot(escrowID string) (holds bool, resolved bool) {
	if el.escrowQuery == nil {
		return false, false
	}
	info, err := el.escrowQuery.GetEscrow(escrowID)
	if err != nil || info == nil {
		return false, false
	}
	addr := el.localParticipantAddress()
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
