package app

import (
	"context"
	"sync"

	sdk "github.com/cosmos/cosmos-sdk/types"
	inferencemodulekeeper "github.com/productscience/inference/x/inference/keeper"
)

// Event/attr names for inference_validation events (must match msg_server_validation.go).
const (
	eventTypeInferenceValidation = "inference_validation"
	attrNeedsRevalidation        = "needs_revalidation"
	attrInferenceID              = "inference_id"
	attrValidator                = "validator"
)

// blockRevalidationEventsCollector accumulates inference_validation events with needs_revalidation=true
// per block (from the PostHandler). At BeginBlock of the next block we use only events from the
// previous block (which is finalized); we clear the current block's buffer at block start so
// events from a non-finalized attempt are discarded.
type blockRevalidationEventsCollector struct {
	mu             sync.Mutex
	eventsByHeight map[int64][]inferencemodulekeeper.RevalidationEventInfo
}

func newBlockRevalidationEventsCollector() *blockRevalidationEventsCollector {
	return &blockRevalidationEventsCollector{
		eventsByHeight: make(map[int64][]inferencemodulekeeper.RevalidationEventInfo),
	}
}

// AddEventsForBlock appends revalidation events for the given block height.
// Called from the PostHandler after each successful tx.
func (c *blockRevalidationEventsCollector) AddEventsForBlock(height int64, events []inferencemodulekeeper.RevalidationEventInfo) {
	if len(events) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.eventsByHeight[height] = append(c.eventsByHeight[height], events...)
}

// GetInferenceValidationRevalidationEvents implements BlockRevalidationEventsProvider.
// Returns events for the given height and removes them (used for the previous block when finalized).
func (c *blockRevalidationEventsCollector) GetInferenceValidationRevalidationEvents(_ context.Context, height int64) ([]inferencemodulekeeper.RevalidationEventInfo, error) {
	c.mu.Lock()
	events := c.eventsByHeight[height]
	delete(c.eventsByHeight, height)
	c.mu.Unlock()
	return events, nil
}

// ClearEventsForHeight discards collected events for the given height. Call at the start of
// BeginBlock for the current block so that if the previous attempt at this block did not
// finalize, we don't use its events; we only keep events from the execution that leads to commit.
func (c *blockRevalidationEventsCollector) ClearEventsForHeight(height int64) {
	c.mu.Lock()
	delete(c.eventsByHeight, height)
	c.mu.Unlock()
}

// setRevalidationEventsFromPostHandler sets the inference keeper's events provider to the
// PostHandler collector and registers the PostHandler. Events are collected per block; when
// the block is finalized (BeginBlock of next block) the hook is called; if the block did
// not finalize, events for that height are discarded at the start of the next attempt.
// The same PostHandler commits or discards the tx-scoped EpochGroupData draft.
func (app *App) setRevalidationEventsAndCommitTxDraftsFromPostHandler() {
	collector := newBlockRevalidationEventsCollector()
	(&app.InferenceKeeper).SetBlockRevalidationEventsProvider(collector)
	app.SetPostHandler(revalidationAndEpochGroupDraftPostHandler(collector, &app.InferenceKeeper))
}

// revalidationAndEpochGroupDraftPostHandler collects revalidation events on success and
// commits the tx-scoped EpochGroupData draft from context on success (on failure the draft is not committed).
func revalidationAndEpochGroupDraftPostHandler(collector *blockRevalidationEventsCollector, keeper *inferencemodulekeeper.Keeper) sdk.PostHandler {
	return func(ctx sdk.Context, _ sdk.Tx, _, success bool) (sdk.Context, error) {
		if success {
			height := ctx.BlockHeight()
			collected := extractRevalidationEventsFromEvents(ctx.EventManager().Events())
			collector.AddEventsForBlock(height, collected)
			keeper.CommitEpochGroupDraftFromContext(ctx)
		}
		return ctx, nil
	}
}

func extractRevalidationEventsFromEvents(events sdk.Events) []inferencemodulekeeper.RevalidationEventInfo {
	var out []inferencemodulekeeper.RevalidationEventInfo
	for _, ev := range events {
		if ev.Type != eventTypeInferenceValidation {
			continue
		}
		var needsRevalidation bool
		var inferenceID, validator string
		for _, attr := range ev.Attributes {
			switch string(attr.Key) {
			case attrNeedsRevalidation:
				needsRevalidation = string(attr.Value) == "true"
			case attrInferenceID:
				inferenceID = string(attr.Value)
			case attrValidator:
				validator = string(attr.Value)
			}
		}
		if needsRevalidation && inferenceID != "" && validator != "" {
			out = append(out, inferencemodulekeeper.RevalidationEventInfo{
				InferenceId: inferenceID,
				Validator:   validator,
			})
		}
	}
	return out
}
