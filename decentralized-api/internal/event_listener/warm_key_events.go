package event_listener

import (
	"fmt"
	"strconv"

	"decentralized-api/apiconfig"
	"decentralized-api/internal/event_listener/chainevents"
)

// The inference module emits these from transactions that change its warm-key
// log (inference-chain/x/inference/keeper/warm_key_log.go). Expiration is
// expires_at on the grant. The chain does not emit an expiry event.
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
	return nil
}

type WarmKeyRevokedEventHandler struct{}

func (e *WarmKeyRevokedEventHandler) GetName() string { return warmKeyRevokedEvent }

func (e *WarmKeyRevokedEventHandler) CanHandle(event *chainevents.JSONRPCResponse) bool {
	return len(event.Result.Events[warmKeyRevokedEvent+".grant_id"]) > 0
}

func (e *WarmKeyRevokedEventHandler) Handle(event *chainevents.JSONRPCResponse, el *EventListener) error {
	return nil
}

func parseWarmKeyEvents(ev map[string][]string, prefix string, kind apiconfig.HostEventKind) ([]apiconfig.HostEvent, error) {
	ids := ev[prefix+".grant_id"]
	if len(ids) == 0 {
		return nil, nil
	}
	out := make([]apiconfig.HostEvent, 0, len(ids))
	for i := range ids {
		payload, err := warmKeyPayloadAt(ev, prefix, i)
		if err != nil {
			return nil, err
		}
		out = append(out, apiconfig.HostEvent{Kind: kind, WarmKey: payload})
	}
	return out, nil
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
