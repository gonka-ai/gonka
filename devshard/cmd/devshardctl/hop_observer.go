package main

import (
	"sync"
	"time"

	"devshard/transport"
)

// gatewayHopObserver implements transport.HopObserver for Step 5g metrics.
type gatewayHopObserver struct {
	metrics        *DevshardMetrics
	participantKey string
	model          string
	sendTime       time.Time

	mu    sync.Mutex
	reqMs int64
}

func newGatewayHopObserver(m *DevshardMetrics, participantKey, model string, sendTime time.Time) *gatewayHopObserver {
	if m == nil {
		return nil
	}
	return &gatewayHopObserver{
		metrics:        m,
		participantKey: participantKey,
		model:          model,
		sendTime:       sendTime,
	}
}

func (o *gatewayHopObserver) OnReqMs(reqMs int64) {
	if o == nil || reqMs <= 0 {
		return
	}
	o.mu.Lock()
	o.reqMs = reqMs
	o.mu.Unlock()
	if !o.sendTime.IsZero() {
		o.metrics.ObserveGatewayHop("gw_to_host", o.participantKey, o.model, "live",
			time.UnixMilli(reqMs).Sub(o.sendTime).Seconds())
	}
}

func (o *gatewayHopObserver) OnChunk(tier string, mlMs, wMs, recvMs int64) {
	if o == nil {
		return
	}
	o.metrics.RecordGatewayHopCoverage("present", o.participantKey, o.model)
	o.mu.Lock()
	reqMs := o.reqMs
	o.mu.Unlock()
	if reqMs > 0 && mlMs >= reqMs {
		o.metrics.ObserveGatewayHop("req_to_ml", o.participantKey, o.model, tier,
			float64(mlMs-reqMs)/1000.0)
	}
	if wMs >= mlMs && mlMs > 0 {
		o.metrics.ObserveGatewayHop("host_buffer", o.participantKey, o.model, tier,
			float64(wMs-mlMs)/1000.0)
	}
	if recvMs >= wMs && wMs > 0 {
		o.metrics.ObserveGatewayHop("host_to_gw", o.participantKey, o.model, tier,
			float64(recvMs-wMs)/1000.0)
	}
}

func (o *gatewayHopObserver) OnChunkAbsent() {
	if o == nil {
		return
	}
	o.metrics.RecordGatewayHopCoverage("absent", o.participantKey, o.model)
}

var _ transport.HopObserver = (*gatewayHopObserver)(nil)
