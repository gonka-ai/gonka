package main

import (
	"context"
	"net/http"

	"devshard/cmd/devshardd/session"
	"devshard/memwatch"
	"devshard/observability"
)

// memoryLogger is the process snapshot. handleDebugMemory serves whatever it
// stored last. A nil logger serves an empty snapshot, which is what a test
// gets before startMemoryLog.
var memoryLogger *memwatch.Logger

// hostInventory is the session-map half of a devshardd snapshot. The JSON
// names are the ones /stats/memory already serves.
type hostInventory struct {
	Sessions         int    `json:"sessions"`
	LiveInferences   int    `json:"live_inferences"`
	SealedInferences int    `json:"sealed_inferences"`
	Mempool          int    `json:"mempool"`
	Executing        int    `json:"executing"`
	Validating       int    `json:"validating"`
	FattestEscrow    string `json:"fattest_escrow,omitempty"`
	FattestLive      int    `json:"fattest_live"`
}

func hostInventoryFrom(c session.SessionMemory) hostInventory {
	return hostInventory{
		Sessions:         c.Sessions,
		LiveInferences:   c.Live,
		SealedInferences: c.Sealed,
		Mempool:          c.Mempool,
		Executing:        c.Executing,
		Validating:       c.Validating,
		FattestEscrow:    c.Fattest,
		FattestLive:      c.FattestLive,
	}
}

func (h hostInventory) LogAttrs() []any {
	return []any{
		"sessions", h.Sessions,
		"live_inferences", h.LiveInferences,
		"sealed_inferences", h.SealedInferences,
		"mempool", h.Mempool,
		"executing", h.Executing,
		"validating", h.Validating,
		"fattest_escrow", h.FattestEscrow,
		"fattest_live", h.FattestLive,
	}
}

func (h hostInventory) publish() {
	observability.SetMemoryInventory(observability.MemoryInventory{
		Sessions:    h.Sessions,
		Live:        h.LiveInferences,
		Sealed:      h.SealedInferences,
		Mempool:     h.Mempool,
		Executing:   h.Executing,
		Validating:  h.Validating,
		Fattest:     h.FattestEscrow,
		FattestLive: h.FattestLive,
	})
}

func startMemoryLog(ctx context.Context, manager *session.HostManager) {
	memoryLogger = memwatch.New(memwatch.Config{
		Inventory: func() memwatch.Inventory {
			var counts session.SessionMemory
			if manager != nil {
				counts = manager.SessionMemoryCounts()
			}
			return hostInventoryFrom(counts)
		},
		OnSample: func(snap memwatch.Snapshot) {
			inv, ok := snap.Inventory().(hostInventory)
			if ok {
				inv.publish()
			}
		},
	})
	memoryLogger.Start(ctx)
}

// handleDebugMemory serves the latest snapshot. It does not take a new one:
// a scrape must not stop the world.
func handleDebugMemory(w http.ResponseWriter, r *http.Request) {
	logger := memoryLogger
	if logger == nil {
		logger = memwatch.New(memwatch.Config{})
	}
	logger.ServeHTTP(w, r)
}
