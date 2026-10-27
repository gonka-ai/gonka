package event_listener

import (
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
	GetEscrow(escrowID string) (*EscrowSlotInfo, error)
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
	// A tx with several MsgCreateDevshardEscrow carries one value per escrow
	// under each key; read them by index like the other batched events.
	for i := range ev[escrowCreatedEvent+".escrow_id"] {
		if err := el.appendEscrowCreated(ev, i); err != nil {
			return err
		}
	}
	return nil
}

func (el *EventListener) appendEscrowCreated(ev map[string][]string, i int) error {
	rawID, _ := getEventValue(ev, escrowCreatedEvent+".escrow_id", i)
	escrowID, err := strconv.ParseUint(rawID, 10, 64)
	if err != nil {
		return fmt.Errorf("parse escrow_id: %w", err)
	}
	rawEpoch, _ := getEventValue(ev, escrowCreatedEvent+".epoch_index", i)
	epochIndex, err := strconv.ParseUint(rawEpoch, 10, 64)
	if err != nil {
		logging.Warn("host_events: malformed epoch_index on escrow_created; defaulting to 0", types.EventProcessing,
			"escrow_id", escrowID, "raw", rawEpoch, "error", err)
		epochIndex = 0
	}
	modelID, _ := getEventValue(ev, escrowCreatedEvent+".model_id", i)
	creator, _ := getEventValue(ev, escrowCreatedEvent+".creator", i)
	amount, _ := getEventValue(ev, escrowCreatedEvent+".amount", i)
	payload := &apiconfig.EscrowPayload{
		EscrowID:   escrowID,
		EpochIndex: epochIndex,
		ModelID:    modelID,
		Creator:    creator,
		Amount:     amount,
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
	// A tx with several MsgSettleDevshardEscrow carries one value per escrow
	// under each key; read them by index like the other batched events.
	for i := range ev[escrowSettledEvent+".escrow_id"] {
		if err := el.appendEscrowSettled(ev, i); err != nil {
			return err
		}
	}
	return nil
}

func (el *EventListener) appendEscrowSettled(ev map[string][]string, i int) error {
	rawID, _ := getEventValue(ev, escrowSettledEvent+".escrow_id", i)
	escrowID, err := strconv.ParseUint(rawID, 10, 64)
	if err != nil {
		return fmt.Errorf("parse escrow_id: %w", err)
	}
	settler, _ := getEventValue(ev, escrowSettledEvent+".settler", i)
	totalPayout, _ := getEventValue(ev, escrowSettledEvent+".total_payout", i)
	fees, _ := getEventValue(ev, escrowSettledEvent+".fees", i)
	remainder, _ := getEventValue(ev, escrowSettledEvent+".remainder", i)
	payload := &apiconfig.EscrowPayload{
		EscrowID:    escrowID,
		Settler:     settler,
		TotalPayout: totalPayout,
		Fees:        fees,
		Remainder:   remainder,
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
