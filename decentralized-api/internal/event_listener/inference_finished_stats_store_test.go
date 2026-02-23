package event_listener

import (
	"database/sql"
	"strconv"
	"testing"
	"time"

	"decentralized-api/internal/event_listener/chainevents"

	_ "modernc.org/sqlite"
)

func TestInferenceFinishedStatsStore_UpsertFromTxEvent(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	store, err := NewInferenceFinishedStatsStore(db)
	if err != nil {
		t.Fatalf("init stats store: %v", err)
	}

	event := &chainevents.JSONRPCResponse{
		Result: chainevents.Result{
			Events: map[string][]string{
				"inference_finished.inference_id":           []string{"inf-1"},
				"inference_finished.requested_by":           []string{"dev-1"},
				"inference_finished.executed_by":            []string{"exec-1"},
				"inference_finished.model":                  []string{"model-a"},
				"inference_finished.epoch_id":               []string{"11"},
				"inference_finished.prompt_token_count":     []string{"100"},
				"inference_finished.completion_token_count": []string{"40"},
				"inference_finished.actual_cost":            []string{"140000"},
				"inference_finished.end_block_timestamp":    []string{"1730000000000"},
				"tx.height":                                 []string{"777"},
			},
		},
	}
	if err := store.UpsertFromTxEvent(event); err != nil {
		t.Fatalf("insert stats: %v", err)
	}

	var (
		requestedBy string
		model       string
		totalTokens int64
		txHeight    int64
	)
	err = db.QueryRow(`
		SELECT requested_by, model, total_tokens, tx_height
		FROM inference_finished_stats
		WHERE inference_id = 'inf-1'
	`).Scan(&requestedBy, &model, &totalTokens, &txHeight)
	if err != nil {
		t.Fatalf("query inserted row: %v", err)
	}
	if requestedBy != "dev-1" || model != "model-a" || totalTokens != 140 || txHeight != 777 {
		t.Fatalf("unexpected row values: requestedBy=%s model=%s totalTokens=%d txHeight=%d", requestedBy, model, totalTokens, txHeight)
	}

	updatedEvent := &chainevents.JSONRPCResponse{
		Result: chainevents.Result{
			Events: map[string][]string{
				"inference_finished.inference_id":           []string{"inf-1"},
				"inference_finished.requested_by":           []string{"dev-2"},
				"inference_finished.executed_by":            []string{"exec-1"},
				"inference_finished.model":                  []string{"model-b"},
				"inference_finished.epoch_id":               []string{"12"},
				"inference_finished.prompt_token_count":     []string{"110"},
				"inference_finished.completion_token_count": []string{"50"},
				"inference_finished.actual_cost":            []string{"160000"},
				"inference_finished.end_block_timestamp":    []string{"1730000001000"},
				"tx.height":                                 []string{"778"},
			},
		},
	}
	if err := store.UpsertFromTxEvent(updatedEvent); err != nil {
		t.Fatalf("update stats: %v", err)
	}

	err = db.QueryRow(`
		SELECT requested_by, model, total_tokens, tx_height
		FROM inference_finished_stats
		WHERE inference_id = 'inf-1'
	`).Scan(&requestedBy, &model, &totalTokens, &txHeight)
	if err != nil {
		t.Fatalf("query updated row: %v", err)
	}
	if requestedBy != "dev-2" || model != "model-b" || totalTokens != 160 || txHeight != 778 {
		t.Fatalf("unexpected updated values: requestedBy=%s model=%s totalTokens=%d txHeight=%d", requestedBy, model, totalTokens, txHeight)
	}
}

func TestInferenceFinishedStatsStore_BackwardCompatibleOldEvent(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	store, err := NewInferenceFinishedStatsStore(db)
	if err != nil {
		t.Fatalf("init stats store: %v", err)
	}

	oldEvent := &chainevents.JSONRPCResponse{
		Result: chainevents.Result{
			Events: map[string][]string{
				"inference_finished.inference_id": []string{"legacy-inf-1"},
			},
		},
	}
	if err := store.UpsertFromTxEvent(oldEvent); err != nil {
		t.Fatalf("old event should be ignored without errors: %v", err)
	}

	var count int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM inference_finished_stats`).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no rows for legacy event, got %d", count)
	}
}

func TestInferenceFinishedStatsStore_UpsertFromTxEvent_MultipleInferencesInTx(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	store, err := NewInferenceFinishedStatsStore(db)
	if err != nil {
		t.Fatalf("init stats store: %v", err)
	}

	event := &chainevents.JSONRPCResponse{
		Result: chainevents.Result{
			Events: map[string][]string{
				"inference_finished.inference_id":           []string{"inf-1", "inf-2"},
				"inference_finished.requested_by":           []string{"dev-1", "dev-2"},
				"inference_finished.executed_by":            []string{"exec-1", "exec-2"},
				"inference_finished.model":                  []string{"model-a", "model-b"},
				"inference_finished.epoch_id":               []string{"11", "12"},
				"inference_finished.prompt_token_count":     []string{"100", "200"},
				"inference_finished.completion_token_count": []string{"40", "50"},
				"inference_finished.actual_cost":            []string{"140000", "250000"},
				"inference_finished.end_block_timestamp":    []string{"1730000000000", "1730000001000"},
				"tx.height":                                 []string{"900"},
			},
		},
	}
	if err := store.UpsertFromTxEvent(event); err != nil {
		t.Fatalf("insert multi inference stats: %v", err)
	}

	var count int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM inference_finished_stats`).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 rows, got %d", count)
	}

	var (
		requestedBy string
		model       string
		totalTokens int64
		txHeight    int64
	)
	err = db.QueryRow(`
		SELECT requested_by, model, total_tokens, tx_height
		FROM inference_finished_stats
		WHERE inference_id = 'inf-2'
	`).Scan(&requestedBy, &model, &totalTokens, &txHeight)
	if err != nil {
		t.Fatalf("query second row: %v", err)
	}
	if requestedBy != "dev-2" || model != "model-b" || totalTokens != 250 || txHeight != 900 {
		t.Fatalf("unexpected second row values: requestedBy=%s model=%s totalTokens=%d txHeight=%d", requestedBy, model, totalTokens, txHeight)
	}
}

func TestInferenceFinishedStatsStore_PrunesOldRowsOnUpsert(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	store, err := NewInferenceFinishedStatsStore(db)
	if err != nil {
		t.Fatalf("init stats store: %v", err)
	}

	oldTimestamp := time.Now().Add(-statsRetentionPeriod - time.Hour).UnixMilli()
	_, err = db.Exec(`
		INSERT INTO inference_finished_stats (
			inference_id, requested_by, executed_by, model, epoch_id,
			prompt_tokens, completion_tokens, total_tokens, actual_cost, end_timestamp_ms, tx_height
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "old-inf", "dev-old", "exec-old", "model-old", 1, 1, 1, 2, 10, oldTimestamp, 1)
	if err != nil {
		t.Fatalf("insert old row: %v", err)
	}

	newTimestamp := time.Now().UnixMilli()
	event := &chainevents.JSONRPCResponse{
		Result: chainevents.Result{
			Events: map[string][]string{
				"inference_finished.inference_id":           []string{"fresh-inf"},
				"inference_finished.requested_by":           []string{"dev-fresh"},
				"inference_finished.executed_by":            []string{"exec-fresh"},
				"inference_finished.model":                  []string{"model-fresh"},
				"inference_finished.epoch_id":               []string{"2"},
				"inference_finished.prompt_token_count":     []string{"10"},
				"inference_finished.completion_token_count": []string{"5"},
				"inference_finished.actual_cost":            []string{"15"},
				"inference_finished.end_block_timestamp":    []string{strconv.FormatInt(newTimestamp, 10)},
				"tx.height":                                 []string{"2"},
			},
		},
	}
	if err := store.UpsertFromTxEvent(event); err != nil {
		t.Fatalf("upsert fresh row: %v", err)
	}

	var oldCount int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM inference_finished_stats WHERE inference_id = 'old-inf'`).Scan(&oldCount); err != nil {
		t.Fatalf("count old row: %v", err)
	}
	if oldCount != 0 {
		t.Fatalf("expected old row to be pruned, got count=%d", oldCount)
	}

	var freshCount int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM inference_finished_stats WHERE inference_id = 'fresh-inf'`).Scan(&freshCount); err != nil {
		t.Fatalf("count fresh row: %v", err)
	}
	if freshCount != 1 {
		t.Fatalf("expected fresh row to remain, got count=%d", freshCount)
	}
}
