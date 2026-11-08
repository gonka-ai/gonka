package store

import (
	"context"
	"testing"
)

// Test flow:
//  1. Upsert two models' filter settings, then upsert the first again with another preset.
//  2. Assert the list holds both, ordered by model, with the replacement preset and its time.
//  3. Delete one twice and assert the second delete still succeeds and one row remains.
func TestModelFiltersUpsertListDelete(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	rows := []ModelFilterRow{
		{Model: "zai-org/GLM-5.3-Flash", Preset: "glm_flash", FiltersJSON: `{}`, UpdatedAt: "2026-10-19T10:00:00Z"},
		{Model: "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8", Preset: "default", FiltersJSON: `{"thinking":"strip"}`, UpdatedAt: "2026-10-19T10:00:00Z"},
		{Model: "zai-org/GLM-5.3-Flash", Preset: "minimax", FiltersJSON: `{"output_tokens_floor":32}`, UpdatedAt: "2026-10-19T11:00:00Z"},
	}
	for _, row := range rows {
		if err := store.UpsertModelFilter(ctx, row); err != nil {
			t.Fatalf("UpsertModelFilter(%q): %v", row.Model, err)
		}
	}

	listed, err := store.ListModelFilters(ctx)
	if err != nil {
		t.Fatalf("ListModelFilters(): %v", err)
	}
	if len(listed) != 2 || listed[0].Model != "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8" || listed[1].Model != "zai-org/GLM-5.3-Flash" {
		t.Fatalf("ListModelFilters() = %+v, want Qwen then GLM", listed)
	}
	if listed[1].Preset != "minimax" || listed[1].FiltersJSON != `{"output_tokens_floor":32}` || listed[1].UpdatedAt != "2026-10-19T11:00:00Z" {
		t.Errorf("GLM row = %+v, want the replacement setting", listed[1])
	}

	for range 2 {
		if err := store.DeleteModelFilter(ctx, "zai-org/GLM-5.3-Flash"); err != nil {
			t.Fatalf("DeleteModelFilter(): %v", err)
		}
	}
	remaining, err := store.ListModelFilters(ctx)
	if err != nil {
		t.Fatalf("ListModelFilters(): %v", err)
	}
	if len(remaining) != 1 {
		t.Errorf("ListModelFilters() = %+v, want one row", remaining)
	}
}
