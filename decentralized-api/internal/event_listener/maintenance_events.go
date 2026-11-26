package event_listener

import (
	"fmt"
	"strconv"
	"strings"

	"common/logging"
	"decentralized-api/apiconfig"
	"decentralized-api/internal/event_listener/chainevents"

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
	return nil
}

type MaintenanceCanceledEventHandler struct{}

func (e *MaintenanceCanceledEventHandler) GetName() string { return "maintenance_canceled" }

func (e *MaintenanceCanceledEventHandler) CanHandle(event *chainevents.JSONRPCResponse) bool {
	return len(event.Result.Events[maintenanceCanceledEvent+".reservation_id"]) > 0
}

func (e *MaintenanceCanceledEventHandler) Handle(event *chainevents.JSONRPCResponse, el *EventListener) error {
	return nil
}

// handleMaintenanceLifecycleEvents used to append BeginBlock cancels from the
// NewBlock socket. The host-event feed reads those from BlockResults instead.
func (el *EventListener) handleMaintenanceLifecycleEvents(event *chainevents.JSONRPCResponse, workerName string) {
}

func parseMaintenanceScheduled(ev map[string][]string) (apiconfig.HostEvent, bool, error) {
	raw := firstAttr(ev, maintenanceScheduledEvent+".reservation_id")
	if raw == "" {
		return apiconfig.HostEvent{}, false, nil
	}
	reservationID, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return apiconfig.HostEvent{}, false, fmt.Errorf("parse reservation_id: %w", err)
	}
	startHeight, _ := strconv.ParseInt(firstAttr(ev, maintenanceScheduledEvent+".start_height"), 10, 64)
	duration, _ := strconv.ParseUint(firstAttr(ev, maintenanceScheduledEvent+".duration_blocks"), 10, 64)
	return apiconfig.HostEvent{
		Kind: apiconfig.HostEventKindMaintenanceScheduled,
		Maintenance: &apiconfig.MaintenancePayload{
			ReservationID:  reservationID,
			Participant:    firstAttr(ev, maintenanceScheduledEvent+".participant"),
			StartHeight:    startHeight,
			DurationBlocks: duration,
		},
	}, true, nil
}

func parseMaintenanceCanceled(ev map[string][]string) (apiconfig.HostEvent, bool, error) {
	raw := firstAttr(ev, maintenanceCanceledEvent+".reservation_id")
	if raw == "" {
		return apiconfig.HostEvent{}, false, nil
	}
	reservationID, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return apiconfig.HostEvent{}, false, fmt.Errorf("parse reservation_id: %w", err)
	}
	durationStr := firstAttr(ev, maintenanceCanceledEvent+".credit_restored")
	if durationStr == "" {
		durationStr = firstAttr(ev, maintenanceCanceledEvent+".credit_refunded")
	}
	duration, _ := strconv.ParseUint(durationStr, 10, 64)
	return apiconfig.HostEvent{
		Kind: apiconfig.HostEventKindMaintenanceCanceled,
		Maintenance: &apiconfig.MaintenancePayload{
			ReservationID:  reservationID,
			Participant:    firstAttr(ev, maintenanceCanceledEvent+".participant"),
			DurationBlocks: duration,
			Reason:         firstAttr(ev, maintenanceCanceledEvent+".reason"),
		},
	}, true, nil
}

func skipForeignMaintenance(addr, participant string) bool {
	if participant == "" || addr == "" {
		return false
	}
	if !strings.EqualFold(participant, addr) {
		logging.Debug("host_events: skip maintenance event for other participant", types.EventProcessing,
			"participant", participant, "local", addr)
		return true
	}
	return false
}
