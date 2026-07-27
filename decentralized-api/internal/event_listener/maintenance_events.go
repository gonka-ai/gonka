package event_listener

import (
	"fmt"
	"strconv"
	"strings"

	"decentralized-api/apiconfig"
	"decentralized-api/internal/event_listener/chainevents"
	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

const (
	maintenanceScheduledEvent = "maintenance_scheduled"
	maintenanceCanceledEvent  = "maintenance_canceled"
)

type MaintenanceScheduledEventHandler struct{}

func (e *MaintenanceScheduledEventHandler) GetName() string { return "maintenance_scheduled" }

func (e *MaintenanceScheduledEventHandler) CanHandle(event *chainevents.JSONRPCResponse) bool {
	return len(event.Result.Events[maintenanceScheduledEvent+".reservation_id"]) > 0
}

func (e *MaintenanceScheduledEventHandler) Handle(event *chainevents.JSONRPCResponse, el *EventListener) error {
	return appendMaintenanceScheduled(el, event.Result.Events)
}

type MaintenanceCanceledEventHandler struct{}

func (e *MaintenanceCanceledEventHandler) GetName() string { return "maintenance_canceled" }

func (e *MaintenanceCanceledEventHandler) CanHandle(event *chainevents.JSONRPCResponse) bool {
	return len(event.Result.Events[maintenanceCanceledEvent+".reservation_id"]) > 0
}

func (e *MaintenanceCanceledEventHandler) Handle(event *chainevents.JSONRPCResponse, el *EventListener) error {
	return appendMaintenanceCanceled(el, event.Result.Events)
}

// handleMaintenanceLifecycleEvents ingests BeginBlock-emitted maintenance_canceled
// (e.g. reason=maintenance_disabled). Tx-path cancels are handled by
// MaintenanceCanceledEventHandler via BlockObserver.
func (el *EventListener) handleMaintenanceLifecycleEvents(event *chainevents.JSONRPCResponse, workerName string) {
	if len(event.Result.Events[maintenanceCanceledEvent+".reservation_id"]) == 0 {
		return
	}
	if err := appendMaintenanceCanceled(el, event.Result.Events); err != nil {
		logging.Error("Failed to process maintenance lifecycle cancel", types.EventProcessing,
			"error", err, "worker", workerName)
	}
}

func appendMaintenanceScheduled(el *EventListener, ev map[string][]string) error {
	if el.hostEvents == nil {
		return nil
	}
	reservationID, err := strconv.ParseUint(firstAttr(ev, maintenanceScheduledEvent+".reservation_id"), 10, 64)
	if err != nil {
		return fmt.Errorf("parse reservation_id: %w", err)
	}
	participant := firstAttr(ev, maintenanceScheduledEvent+".participant")
	if skipForeignMaintenance(el, participant) {
		return nil
	}
	startHeight, _ := strconv.ParseInt(firstAttr(ev, maintenanceScheduledEvent+".start_height"), 10, 64)
	duration, _ := strconv.ParseUint(firstAttr(ev, maintenanceScheduledEvent+".duration_blocks"), 10, 64)

	el.hostEvents.Append(apiconfig.HostEvent{
		Kind: apiconfig.HostEventKindMaintenanceScheduled,
		Maintenance: &apiconfig.MaintenancePayload{
			ReservationID:  reservationID,
			Participant:    participant,
			StartHeight:    startHeight,
			DurationBlocks: duration,
		},
	})
	return nil
}

func appendMaintenanceCanceled(el *EventListener, ev map[string][]string) error {
	if el.hostEvents == nil {
		return nil
	}
	reservationID, err := strconv.ParseUint(firstAttr(ev, maintenanceCanceledEvent+".reservation_id"), 10, 64)
	if err != nil {
		return fmt.Errorf("parse reservation_id: %w", err)
	}
	participant := firstAttr(ev, maintenanceCanceledEvent+".participant")
	if skipForeignMaintenance(el, participant) {
		return nil
	}
	// Tx cancel uses credit_restored; lifecycle disabled uses credit_refunded.
	durationStr := firstAttr(ev, maintenanceCanceledEvent+".credit_restored")
	if durationStr == "" {
		durationStr = firstAttr(ev, maintenanceCanceledEvent+".credit_refunded")
	}
	duration, _ := strconv.ParseUint(durationStr, 10, 64)

	el.hostEvents.Append(apiconfig.HostEvent{
		Kind: apiconfig.HostEventKindMaintenanceCanceled,
		Maintenance: &apiconfig.MaintenancePayload{
			ReservationID:  reservationID,
			Participant:    participant,
			DurationBlocks: duration,
			Reason:         firstAttr(ev, maintenanceCanceledEvent+".reason"),
		},
	})
	return nil
}

func skipForeignMaintenance(el *EventListener, participant string) bool {
	if participant == "" {
		return false
	}
	addr := el.localParticipantAddress()
	if addr == "" {
		return false
	}
	if !strings.EqualFold(participant, addr) {
		logging.Debug("host_events: skip maintenance event for other participant", types.EventProcessing,
			"participant", participant, "local", addr)
		return true
	}
	return false
}
