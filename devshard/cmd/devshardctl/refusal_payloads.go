package main

import "devshard/host"

func (e *Redundancy) rememberRefusalPayload(nonce uint64, payload *host.InferencePayload) {
	if e == nil || payload == nil {
		return
	}
	e.refusalPayloadMu.Lock()
	defer e.refusalPayloadMu.Unlock()
	if e.refusalPayloads == nil {
		e.refusalPayloads = make(map[uint64]*host.InferencePayload)
	}
	e.refusalPayloads[nonce] = payload
}

func (e *Redundancy) refusalPayload(nonce uint64) (*host.InferencePayload, bool) {
	if e == nil {
		return nil, false
	}
	e.refusalPayloadMu.Lock()
	defer e.refusalPayloadMu.Unlock()
	payload, kept := e.refusalPayloads[nonce]
	return payload, kept
}

func (e *Redundancy) hasRefusalPayload(nonce uint64) bool {
	_, kept := e.refusalPayload(nonce)
	return kept
}

func (e *Redundancy) forgetRefusalPayload(nonce uint64) {
	if e == nil {
		return
	}
	e.refusalPayloadMu.Lock()
	defer e.refusalPayloadMu.Unlock()
	delete(e.refusalPayloads, nonce)
}

func (e *Redundancy) forgetRefusalPayloadsUnless(stillPending func(nonce uint64) bool) {
	if e == nil {
		return
	}
	e.refusalPayloadMu.Lock()
	defer e.refusalPayloadMu.Unlock()
	for nonce := range e.refusalPayloads {
		if !stillPending(nonce) {
			delete(e.refusalPayloads, nonce)
		}
	}
}
