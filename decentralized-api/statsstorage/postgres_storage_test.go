package statsstorage

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// testPool returns a *pgxpool.Pool for integration tests.
// Skips the test if TEST_DATABASE_URL is not set.
// Each test gets clean devshard tables (inference_stats is left untouched to
// verify no interference).
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping Postgres integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	// Ensure schema is created.
	s := &PostgresStorage{pool: pool}
	require.NoError(t, s.ensureSchema(ctx))

	// Clean devshard tables between tests.
	_, err = pool.Exec(ctx, "DELETE FROM devshard_model_stats")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, "DELETE FROM devshard_escrow_stats")
	require.NoError(t, err)

	return pool
}

func TestPostgresUpsertDevshardEscrow_SingleEscrow(t *testing.T) {
	pool := testPool(t)
	s := &PostgresStorage{pool: pool}
	ctx := context.Background()

	escrow := DevshardEscrow{
		EscrowID:                  "esc-1",
		EpochID:                   10,
		Participant:               "gonka1dev",
		TotalCostInCoins:          5000,
		TotalInferences:           25,
		TotalPromptTokenCount:     1000,
		TotalCompletionTokenCount: 2000,
		TotalTokenCount:           3000,
		SettlementTimestamp:       1700000000000,
	}
	models := []DevshardModelAggregate{
		{
			Model:                "gpt-4",
			PromptTokenCount:     600,
			CompletionTokenCount: 1200,
			TotalTokenCount:      1800,
			Inferences:           15,
			CostInCoins:          3000,
		},
		{
			Model:                "llama-3",
			PromptTokenCount:     400,
			CompletionTokenCount: 800,
			TotalTokenCount:      1200,
			Inferences:           10,
			CostInCoins:          2000,
		},
	}

	err := s.UpsertDevshardEscrow(ctx, escrow, models)
	require.NoError(t, err)

	// Verify escrow row was persisted.
	var (
		gotEscrowID    string
		gotParticipant string
		gotInferences  int32
		gotTokens      uint64
		gotCost        int64
		gotTimestamp   int64
	)
	err = pool.QueryRow(ctx,
		`SELECT escrow_id, participant, total_inferences, total_token_count, total_cost_in_coins, settlement_timestamp
		 FROM devshard_escrow_stats WHERE escrow_id = $1 AND epoch_id = $2`, "esc-1", 10,
	).Scan(&gotEscrowID, &gotParticipant, &gotInferences, &gotTokens, &gotCost, &gotTimestamp)
	require.NoError(t, err)
	require.Equal(t, "esc-1", gotEscrowID)
	require.Equal(t, "gonka1dev", gotParticipant)
	require.Equal(t, int32(25), gotInferences)
	require.Equal(t, uint64(3000), gotTokens)
	require.Equal(t, int64(5000), gotCost)
	require.Equal(t, int64(1700000000000), gotTimestamp)

	// Verify model rows were persisted.
	var modelCount int
	err = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM devshard_model_stats WHERE escrow_id = $1 AND epoch_id = $2`, "esc-1", 10,
	).Scan(&modelCount)
	require.NoError(t, err)
	require.Equal(t, 2, modelCount)

	// Verify specific model row.
	var gotModelTokens uint64
	var gotModelInferences int32
	err = pool.QueryRow(ctx,
		`SELECT total_token_count, inferences FROM devshard_model_stats
		 WHERE escrow_id = $1 AND epoch_id = $2 AND model = $3`, "esc-1", 10, "gpt-4",
	).Scan(&gotModelTokens, &gotModelInferences)
	require.NoError(t, err)
	require.Equal(t, uint64(1800), gotModelTokens)
	require.Equal(t, int32(15), gotModelInferences)
}

func TestPostgresUpsertDevshardEscrow_Idempotent(t *testing.T) {
	pool := testPool(t)
	s := &PostgresStorage{pool: pool}
	ctx := context.Background()

	escrow := DevshardEscrow{
		EscrowID:                  "esc-idem",
		EpochID:                   5,
		Participant:               "gonka1a",
		TotalCostInCoins:          100,
		TotalInferences:           2,
		TotalPromptTokenCount:     10,
		TotalCompletionTokenCount: 20,
		TotalTokenCount:           30,
		SettlementTimestamp:       1700000001000,
	}
	models := []DevshardModelAggregate{
		{Model: "model-x", PromptTokenCount: 10, CompletionTokenCount: 20, TotalTokenCount: 30, Inferences: 2, CostInCoins: 100},
	}

	// First upsert.
	require.NoError(t, s.UpsertDevshardEscrow(ctx, escrow, models))

	// Second upsert with updated cost — should overwrite, not duplicate.
	escrow.TotalCostInCoins = 200
	models[0].CostInCoins = 200
	require.NoError(t, s.UpsertDevshardEscrow(ctx, escrow, models))

	// There should be exactly one escrow row.
	var cnt int
	err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM devshard_escrow_stats WHERE escrow_id = $1 AND epoch_id = $2`, "esc-idem", 5,
	).Scan(&cnt)
	require.NoError(t, err)
	require.Equal(t, 1, cnt)

	// The cost should be the updated value.
	var gotCost int64
	err = pool.QueryRow(ctx,
		`SELECT total_cost_in_coins FROM devshard_escrow_stats WHERE escrow_id = $1 AND epoch_id = $2`, "esc-idem", 5,
	).Scan(&gotCost)
	require.NoError(t, err)
	require.Equal(t, int64(200), gotCost)

	// Model row should also be exactly one.
	err = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM devshard_model_stats WHERE escrow_id = $1 AND epoch_id = $2`, "esc-idem", 5,
	).Scan(&cnt)
	require.NoError(t, err)
	require.Equal(t, 1, cnt)
}

func TestPostgresUpsertDevshardEscrow_ModelSetReplacement(t *testing.T) {
	pool := testPool(t)
	s := &PostgresStorage{pool: pool}
	ctx := context.Background()

	escrow := DevshardEscrow{
		EscrowID:                  "esc-replace",
		EpochID:                   8,
		Participant:               "gonka1c",
		TotalCostInCoins:          500,
		TotalInferences:           10,
		TotalPromptTokenCount:     100,
		TotalCompletionTokenCount: 200,
		TotalTokenCount:           300,
		SettlementTimestamp:       1700000003000,
	}

	// First upsert: two models.
	twoModels := []DevshardModelAggregate{
		{Model: "gpt-4", PromptTokenCount: 60, CompletionTokenCount: 120, TotalTokenCount: 180, Inferences: 6, CostInCoins: 300},
		{Model: "llama-3", PromptTokenCount: 40, CompletionTokenCount: 80, TotalTokenCount: 120, Inferences: 4, CostInCoins: 200},
	}
	require.NoError(t, s.UpsertDevshardEscrow(ctx, escrow, twoModels))

	var cnt int
	err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM devshard_model_stats WHERE escrow_id = $1 AND epoch_id = $2`, "esc-replace", 8,
	).Scan(&cnt)
	require.NoError(t, err)
	require.Equal(t, 2, cnt, "first upsert should produce 2 model rows")

	// Second upsert: only one model — the stale "llama-3" row must be removed.
	escrow.TotalCostInCoins = 300
	oneModel := []DevshardModelAggregate{
		{Model: "gpt-4", PromptTokenCount: 100, CompletionTokenCount: 200, TotalTokenCount: 300, Inferences: 10, CostInCoins: 300},
	}
	require.NoError(t, s.UpsertDevshardEscrow(ctx, escrow, oneModel))

	err = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM devshard_model_stats WHERE escrow_id = $1 AND epoch_id = $2`, "esc-replace", 8,
	).Scan(&cnt)
	require.NoError(t, err)
	require.Equal(t, 1, cnt, "second upsert with smaller model set must remove stale rows")

	// The surviving row should be the updated gpt-4.
	var gotTokens uint64
	err = pool.QueryRow(ctx,
		`SELECT total_token_count FROM devshard_model_stats WHERE escrow_id = $1 AND epoch_id = $2 AND model = $3`,
		"esc-replace", 8, "gpt-4",
	).Scan(&gotTokens)
	require.NoError(t, err)
	require.Equal(t, uint64(300), gotTokens)
}

func TestPostgresUpsertDevshardEscrow_NoModels(t *testing.T) {
	pool := testPool(t)
	s := &PostgresStorage{pool: pool}
	ctx := context.Background()

	escrow := DevshardEscrow{
		EscrowID:            "esc-nomodel",
		EpochID:             7,
		Participant:         "gonka1b",
		TotalCostInCoins:    0,
		TotalInferences:     0,
		TotalTokenCount:     0,
		SettlementTimestamp: 1700000002000,
	}

	err := s.UpsertDevshardEscrow(ctx, escrow, nil)
	require.NoError(t, err)

	// Escrow row exists.
	var cnt int
	err = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM devshard_escrow_stats WHERE escrow_id = $1`, "esc-nomodel",
	).Scan(&cnt)
	require.NoError(t, err)
	require.Equal(t, 1, cnt)

	// No model rows.
	err = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM devshard_model_stats WHERE escrow_id = $1`, "esc-nomodel",
	).Scan(&cnt)
	require.NoError(t, err)
	require.Equal(t, 0, cnt)
}

func TestPostgresPruneOlderThan_IncludesDevshardTables(t *testing.T) {
	pool := testPool(t)
	s := &PostgresStorage{pool: pool}
	ctx := context.Background()

	// Insert two escrows: one old, one recent.
	oldEscrow := DevshardEscrow{
		EscrowID:            "esc-old",
		EpochID:             1,
		Participant:         "gonka1old",
		TotalCostInCoins:    100,
		TotalInferences:     1,
		TotalTokenCount:     10,
		SettlementTimestamp: 1000, // very old
	}
	recentEscrow := DevshardEscrow{
		EscrowID:            "esc-new",
		EpochID:             2,
		Participant:         "gonka1new",
		TotalCostInCoins:    200,
		TotalInferences:     2,
		TotalTokenCount:     20,
		SettlementTimestamp: 9999999999999, // far future
	}
	oldModels := []DevshardModelAggregate{
		{Model: "old-model", TotalTokenCount: 10, Inferences: 1, CostInCoins: 100},
	}
	newModels := []DevshardModelAggregate{
		{Model: "new-model", TotalTokenCount: 20, Inferences: 2, CostInCoins: 200},
	}

	require.NoError(t, s.UpsertDevshardEscrow(ctx, oldEscrow, oldModels))
	require.NoError(t, s.UpsertDevshardEscrow(ctx, recentEscrow, newModels))

	// Prune with cutoff that removes the old escrow.
	err := s.PruneOlderThan(ctx, 5000)
	require.NoError(t, err)

	// Old escrow and its model should be gone.
	var cnt int
	err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM devshard_escrow_stats WHERE escrow_id = $1`, "esc-old").Scan(&cnt)
	require.NoError(t, err)
	require.Equal(t, 0, cnt)

	err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM devshard_model_stats WHERE escrow_id = $1`, "esc-old").Scan(&cnt)
	require.NoError(t, err)
	require.Equal(t, 0, cnt)

	// Recent escrow and its model should remain.
	err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM devshard_escrow_stats WHERE escrow_id = $1`, "esc-new").Scan(&cnt)
	require.NoError(t, err)
	require.Equal(t, 1, cnt)

	err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM devshard_model_stats WHERE escrow_id = $1`, "esc-new").Scan(&cnt)
	require.NoError(t, err)
	require.Equal(t, 1, cnt)
}

func TestPostgresUpsertDevshardEscrow_DoesNotAffectInferenceStats(t *testing.T) {
	pool := testPool(t)
	s := &PostgresStorage{pool: pool}
	ctx := context.Background()

	// Insert an inference record first.
	infRec := InferenceRecord{
		InferenceID:          "inf-isolation-test",
		RequestedBy:          "gonka1dev",
		Model:                "gpt-4",
		Status:               "FINISHED",
		EpochID:              10,
		PromptTokenCount:     100,
		CompletionTokenCount: 50,
		TotalTokenCount:      150,
		ActualCostInCoins:    999,
		StartBlockTimestamp:  1700000000000,
		EndBlockTimestamp:    1700000001000,
	}
	require.NoError(t, s.UpsertInference(ctx, infRec))

	// Now upsert a devshard escrow.
	escrow := DevshardEscrow{
		EscrowID:            "esc-iso",
		EpochID:             10,
		Participant:         "gonka1dev",
		TotalCostInCoins:    5000,
		TotalInferences:     25,
		TotalTokenCount:     3000,
		SettlementTimestamp: 1700000002000,
	}
	require.NoError(t, s.UpsertDevshardEscrow(ctx, escrow, nil))

	// Verify the inference record is untouched.
	recs, err := s.GetDeveloperInferencesByTime(ctx, "gonka1dev", 0, 9999999999999)
	require.NoError(t, err)
	found := false
	for _, r := range recs {
		if r.InferenceID == "inf-isolation-test" {
			found = true
			require.Equal(t, int64(999), r.ActualCostInCoins)
		}
	}
	require.True(t, found, "inference record should not be affected by devshard escrow upsert")

	// Clean up the test inference record.
	_, _ = pool.Exec(ctx, `DELETE FROM inference_stats WHERE inference_id = $1`, "inf-isolation-test")
}

func TestPostgresEpochRangeQueries_RespectExclusiveInclusiveBounds(t *testing.T) {
	pool := testPool(t)
	s := &PostgresStorage{pool: pool}
	ctx := context.Background()

	// Ensure deterministic range assertions.
	_, err := pool.Exec(ctx, `DELETE FROM inference_stats`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM devshard_model_stats`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM devshard_escrow_stats`)
	require.NoError(t, err)

	seedInference := func(id string, epoch uint64, tokens uint64, cost int64) {
		require.NoError(t, s.UpsertInference(ctx, InferenceRecord{
			InferenceID:          id,
			RequestedBy:          "gonka1bounds",
			Model:                "model-a",
			Status:               "FINISHED",
			EpochID:              epoch,
			PromptTokenCount:     tokens,
			CompletionTokenCount: 0,
			TotalTokenCount:      tokens,
			ActualCostInCoins:    cost,
			StartBlockTimestamp:  1700000000000,
			EndBlockTimestamp:    1700000001000,
			InferenceTimestamp:   1700000001000,
		}))
	}
	seedInference("inf-bounds-90", 90, 90, 9)
	seedInference("inf-bounds-95", 95, 95, 10)
	seedInference("inf-bounds-100", 100, 100, 11)

	require.NoError(t, s.UpsertDevshardEscrow(ctx, DevshardEscrow{
		EscrowID:            "esc-bounds-90",
		EpochID:             90,
		Participant:         "gonka1bounds",
		TotalCostInCoins:    90,
		TotalInferences:     9,
		TotalTokenCount:     900,
		SettlementTimestamp: 1700000002000,
	}, nil))
	require.NoError(t, s.UpsertDevshardEscrow(ctx, DevshardEscrow{
		EscrowID:            "esc-bounds-98",
		EpochID:             98,
		Participant:         "gonka1bounds",
		TotalCostInCoins:    12,
		TotalInferences:     2,
		TotalTokenCount:     180,
		SettlementTimestamp: 1700000003000,
	}, nil))

	mainnetSummary, err := s.GetSummaryByEpochRange(ctx, 95, 100)
	require.NoError(t, err)
	require.Equal(t, int64(100), mainnetSummary.AiTokens)
	require.Equal(t, int32(1), mainnetSummary.Inferences)
	require.Equal(t, int64(11), mainnetSummary.ActualInferencesCost)

	devshardSummary, err := s.GetDevshardSummaryByEpochRange(ctx, 95, 100)
	require.NoError(t, err)
	require.Equal(t, int64(180), devshardSummary.AiTokens)
	require.Equal(t, int32(2), devshardSummary.Inferences)
	require.Equal(t, int64(12), devshardSummary.ActualInferencesCost)
}

func TestPostgresDeveloperEpochRangeQueries_FilterByDeveloperAndBounds(t *testing.T) {
	pool := testPool(t)
	s := &PostgresStorage{pool: pool}
	ctx := context.Background()

	// Ensure deterministic developer/range assertions.
	_, err := pool.Exec(ctx, `DELETE FROM inference_stats`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM devshard_model_stats`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM devshard_escrow_stats`)
	require.NoError(t, err)

	require.NoError(t, s.UpsertInference(ctx, InferenceRecord{
		InferenceID:          "inf-dev-a-100",
		RequestedBy:          "gonka1a",
		Model:                "model-a",
		Status:               "FINISHED",
		EpochID:              100,
		PromptTokenCount:     120,
		CompletionTokenCount: 0,
		TotalTokenCount:      120,
		ActualCostInCoins:    12,
		StartBlockTimestamp:  1700000000000,
		EndBlockTimestamp:    1700000001000,
		InferenceTimestamp:   1700000001000,
	}))
	require.NoError(t, s.UpsertInference(ctx, InferenceRecord{
		InferenceID:          "inf-dev-a-95",
		RequestedBy:          "gonka1a",
		Model:                "model-a",
		Status:               "FINISHED",
		EpochID:              95,
		PromptTokenCount:     95,
		CompletionTokenCount: 0,
		TotalTokenCount:      95,
		ActualCostInCoins:    9,
		StartBlockTimestamp:  1700000000000,
		EndBlockTimestamp:    1700000001000,
		InferenceTimestamp:   1700000001000,
	}))
	require.NoError(t, s.UpsertInference(ctx, InferenceRecord{
		InferenceID:          "inf-dev-b-100",
		RequestedBy:          "gonka1b",
		Model:                "model-b",
		Status:               "FINISHED",
		EpochID:              100,
		PromptTokenCount:     999,
		CompletionTokenCount: 0,
		TotalTokenCount:      999,
		ActualCostInCoins:    99,
		StartBlockTimestamp:  1700000000000,
		EndBlockTimestamp:    1700000001000,
		InferenceTimestamp:   1700000001000,
	}))

	require.NoError(t, s.UpsertDevshardEscrow(ctx, DevshardEscrow{
		EscrowID:            "esc-dev-a-98",
		EpochID:             98,
		Participant:         "gonka1a",
		TotalCostInCoins:    7,
		TotalInferences:     1,
		TotalTokenCount:     70,
		SettlementTimestamp: 1700000003000,
	}, nil))
	require.NoError(t, s.UpsertDevshardEscrow(ctx, DevshardEscrow{
		EscrowID:            "esc-dev-a-90",
		EpochID:             90,
		Participant:         "gonka1a",
		TotalCostInCoins:    70,
		TotalInferences:     7,
		TotalTokenCount:     700,
		SettlementTimestamp: 1700000002000,
	}, nil))
	require.NoError(t, s.UpsertDevshardEscrow(ctx, DevshardEscrow{
		EscrowID:            "esc-dev-b-98",
		EpochID:             98,
		Participant:         "gonka1b",
		TotalCostInCoins:    80,
		TotalInferences:     8,
		TotalTokenCount:     800,
		SettlementTimestamp: 1700000003000,
	}, nil))

	mainnetSummary, err := s.GetSummaryByDeveloperEpochRange(ctx, "gonka1a", 95, 100)
	require.NoError(t, err)
	require.Equal(t, int64(120), mainnetSummary.AiTokens)
	require.Equal(t, int32(1), mainnetSummary.Inferences)
	require.Equal(t, int64(12), mainnetSummary.ActualInferencesCost)

	devshardSummary, err := s.GetDevshardSummaryByDeveloperEpochRange(ctx, "gonka1a", 95, 100)
	require.NoError(t, err)
	require.Equal(t, int64(70), devshardSummary.AiTokens)
	require.Equal(t, int32(1), devshardSummary.Inferences)
	require.Equal(t, int64(7), devshardSummary.ActualInferencesCost)
}
