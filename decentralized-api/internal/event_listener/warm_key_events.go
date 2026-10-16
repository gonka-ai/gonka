package event_listener

import (
	"fmt"
	"strconv"

	"common/logging"
	"decentralized-api/apiconfig"
	"decentralized-api/internal/event_listener/chainevents"

	"github.com/productscience/inference/x/inference/types"
)

// The inference module emits these on every change of its warm-key log
// (inference-chain/x/inference/keeper/warm_key_log.go). Grants and revokes
// come from transactions; an expiry comes from EndBlock, which the NewBlock
// path handles through handleWarmKeyExpiryEvents.
const (
	warmKeyGrantedEvent = "warm_key_granted"
	warmKeyRevokedEvent = "warm_key_revoked"
)

type WarmKeyGrantedEventHandler struct{}

func (e *WarmKeyGrantedEventHandler) GetName() string { return warmKeyGrantedEvent }

func (e *WarmKeyGrantedEventHandler) CanHandle(event *chainevents.JSONRPCResponse) bool {
	return len(event.Result.Events[warmKeyGrantedEvent+".grant_id"]) > 0
}

func (e *WarmKeyGrantedEventHandler) Handle(event *chainevents.JSONRPCResponse, el *EventListener) error {
	return appendWarmKeyEvents(el, event.Result.Events, warmKeyGrantedEvent, apiconfig.HostEventKindWarmKeyGranted)
}

type WarmKeyRevokedEventHandler struct{}

func (e *WarmKeyRevokedEventHandler) GetName() string { return warmKeyRevokedEvent }

func (e *WarmKeyRevokedEventHandler) CanHandle(event *chainevents.JSONRPCResponse) bool {
	return len(event.Result.Events[warmKeyRevokedEvent+".grant_id"]) > 0
}

func (e *WarmKeyRevokedEventHandler) Handle(event *chainevents.JSONRPCResponse, el *EventListener) error {
	return appendWarmKeyEvents(el, event.Result.Events, warmKeyRevokedEvent, apiconfig.HostEventKindWarmKeyRevoked)
}

// handleWarmKeyExpiryEvents ingests the EndBlock-emitted warm_key_revoked
// (end = EXPIRED). Tx-path ends are handled by WarmKeyRevokedEventHandler via
// BlockObserver.
func (el *EventListener) handleWarmKeyExpiryEvents(event *chainevents.JSONRPCResponse, workerName string) {
	if len(event.Result.Events[warmKeyRevokedEvent+".grant_id"]) == 0 {
		return
	}
	if err := appendWarmKeyEvents(el, event.Result.Events, warmKeyRevokedEvent, apiconfig.HostEventKindWarmKeyRevoked); err != nil {
		logging.Error("Failed to process warm-key expiry events", types.EventProcessing,
			"error", err, "worker", workerName)
	}
}

// appendWarmKeyEvents appends one host event per attribute set of prefix. A
// block (or a transaction with several grants) carries the attributes as
// parallel lists, index i of each belonging to the same entry. Nothing is
// filtered by participant: a host needs the keys of every peer in its groups.
func appendWarmKeyEvents(el *EventListener, ev map[string][]string, prefix string, kind apiconfig.HostEventKind) error {
	if el.hostEvents == nil {
		return nil
	}
	ids := ev[prefix+".grant_id"]
	for i := range ids {
		payload, err := warmKeyPayloadAt(ev, prefix, i)
		if err != nil {
			return err
		}
		el.hostEvents.Append(apiconfig.HostEvent{Kind: kind, WarmKey: payload})
	}
	return nil
}

func warmKeyPayloadAt(ev map[string][]string, prefix string, i int) (*apiconfig.WarmKeyPayload, error) {
	grantID, err := strconv.ParseUint(nthAttr(ev, prefix+".grant_id", i), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse %s.grant_id: %w", prefix, err)
	}
	height, err := strconv.ParseInt(nthAttr(ev, prefix+".height", i), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse %s.height: %w", prefix, err)
	}
	payload := &apiconfig.WarmKeyPayload{
		Granter: nthAttr(ev, prefix+".granter", i),
		Grantee: nthAttr(ev, prefix+".grantee", i),
		GrantID: grantID,
		Height:  height,
		End:     nthAttr(ev, prefix+".end", i),
	}
	if payload.Granter == "" || payload.Grantee == "" {
		return nil, fmt.Errorf("%s: granter or grantee missing", prefix)
	}
	if raw := nthAttr(ev, prefix+".expires_at", i); raw != "" {
		expires, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse %s.expires_at: %w", prefix, err)
		}
		payload.ExpiresAtUnix = expires
	}
	return payload, nil
}

func nthAttr(events map[string][]string, key string, i int) string {
	if vals := events[key]; i < len(vals) {
		return vals[i]
	}
	return ""
}
