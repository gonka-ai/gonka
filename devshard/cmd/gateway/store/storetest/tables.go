package storetest

import (
	"context"
	"slices"
	"testing"
	"time"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/env"
	"devshard/cmd/gateway/store"
)

// Test flow:
//  1. Load overrides from a fresh store and assert they come back zero-valued.
//  2. Save an override and load it back, asserting it matches.
//  3. Save a second override and load again, asserting it replaces rather than appends to the first.
func overridesRoundTripAndEmptyLoad(t *testing.T, open Opener) {
	testStore := open()
	ctx := context.Background()

	empty, err := testStore.LoadOverrides(ctx)
	if err != nil {
		t.Fatalf("LoadOverrides() on fresh store: %v", err)
	}
	if empty.DefaultMaxTokens != nil {
		t.Fatal("fresh store must return zero-value overrides")
	}

	maxTokens := int64(1234)
	saved := config.Overrides{Tunables: env.Tunables{DefaultMaxTokens: &maxTokens}}
	if err := testStore.SaveOverrides(ctx, saved); err != nil {
		t.Fatalf("SaveOverrides(): %v", err)
	}
	loaded, err := testStore.LoadOverrides(ctx)
	if err != nil {
		t.Fatalf("LoadOverrides() after save: %v", err)
	}
	if loaded.DefaultMaxTokens == nil || *loaded.DefaultMaxTokens != 1234 {
		t.Fatalf("loaded overrides = %+v, want DefaultMaxTokens 1234", loaded)
	}

	newTokens := int64(999)
	if err := testStore.SaveOverrides(ctx, config.Overrides{Tunables: env.Tunables{DefaultMaxTokens: &newTokens}}); err != nil {
		t.Fatalf("second SaveOverrides(): %v", err)
	}
	replaced, err := testStore.LoadOverrides(ctx)
	if err != nil {
		t.Fatalf("LoadOverrides() after replace: %v", err)
	}
	if replaced.DefaultMaxTokens == nil || *replaced.DefaultMaxTokens != 999 {
		t.Fatalf("replaced overrides = %+v, want 999", replaced)
	}
}

// Test flow:
//  1. Save overrides that carry per-model settings, a legacy model limit and a legacy model access tier, and close the store.
//  2. Open a new store on the same database and load the overrides.
//  3. Assert the per-model setting, the legacy limit and the legacy tier come back as saved.
func overridesKeepPerModelSettingsAndLegacyKeysAcrossAReopen(t *testing.T, open Opener) {
	ctx := context.Background()
	maxBurns := int64(3)
	first := open()
	saved := config.Overrides{
		Models:      map[string]config.ModelSettings{"fake-model-a": {MaxBurnsPerRequest: &maxBurns}},
		ModelLimits: map[string]config.LegacyModelLimits{"fake-model-b": {DefaultMaxTokens: 100, MaxTokensCap: 200}},
		ModelAccess: config.LegacyModelAccess{"fake-model-b": "api_key"},
	}
	if err := first.SaveOverrides(ctx, saved); err != nil {
		t.Fatalf("SaveOverrides(): %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	second := open()
	t.Cleanup(func() { _ = second.Close() })
	loaded, err := second.LoadOverrides(ctx)
	if err != nil {
		t.Fatalf("LoadOverrides(): %v", err)
	}
	if got := loaded.Models["fake-model-a"].MaxBurnsPerRequest; got == nil || *got != maxBurns {
		t.Fatalf("Models[fake-model-a].MaxBurnsPerRequest = %v, want %d", got, maxBurns)
	}
	if got := loaded.ModelLimits["fake-model-b"]; got.DefaultMaxTokens != 100 || got.MaxTokensCap != 200 {
		t.Fatalf("ModelLimits[fake-model-b] = %+v, want 100 and 200", got)
	}
	if got := loaded.ModelAccess["fake-model-b"]; got != "api_key" {
		t.Fatalf("ModelAccess[fake-model-b] = %q, want api_key", got)
	}
}

// Test flow:
//  1. Upsert two models' filter settings, then upsert the first again with another preset.
//  2. Assert the list holds both, ordered by model, with the replacement preset and its time.
//  3. Delete one twice and assert the second delete still succeeds and one row remains.
func modelFiltersUpsertListDelete(t *testing.T, open Opener) {
	testStore := open()
	ctx := context.Background()
	rows := []store.ModelFilterRow{
		{Model: "zai-org/GLM-5.3-Flash", Preset: "glm_flash", FiltersJSON: `{}`, UpdatedAt: "2026-10-19T10:00:00Z"},
		{Model: "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8", Preset: "default", FiltersJSON: `{"thinking":"strip"}`, UpdatedAt: "2026-10-19T10:00:00Z"},
		{Model: "zai-org/GLM-5.3-Flash", Preset: "minimax", FiltersJSON: `{"output_tokens_floor":32}`, UpdatedAt: "2026-10-19T11:00:00Z"},
	}
	for _, row := range rows {
		if err := testStore.UpsertModelFilter(ctx, row); err != nil {
			t.Fatalf("UpsertModelFilter(%q): %v", row.Model, err)
		}
	}

	listed, err := testStore.ListModelFilters(ctx)
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
		if err := testStore.DeleteModelFilter(ctx, "zai-org/GLM-5.3-Flash"); err != nil {
			t.Fatalf("DeleteModelFilter(): %v", err)
		}
	}
	remaining, err := testStore.ListModelFilters(ctx)
	if err != nil {
		t.Fatalf("ListModelFilters(): %v", err)
	}
	if len(remaining) != 1 {
		t.Errorf("ListModelFilters() = %+v, want one row", remaining)
	}
}

// Test flow:
//  1. Save a `store.RotationStatus` row for one model and role.
//  2. Save a second row for the same model and role with a different stage, epoch, completion, and timestamp.
//  3. Assert `LoadRotationStatuses` returns exactly one row, holding the second save's fields.
func rotationStatusUpsertByModelRoleReplaces(t *testing.T, open Opener) {
	testStore := open()
	ctx := context.Background()

	first := store.RotationStatus{
		Model:       "model-a",
		Role:        "temp",
		Stage:       "prepare_temp",
		Epoch:       3,
		Completed:   false,
		CreateError: "model not served by network",
		UpdatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := testStore.SaveRotationStatus(ctx, first); err != nil {
		t.Fatalf("SaveRotationStatus() first: %v", err)
	}

	second := first
	second.Stage = "finish_regular"
	second.Epoch = 4
	second.Completed = true
	second.CreateError = ""
	second.UpdatedAt = time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC)
	if err := testStore.SaveRotationStatus(ctx, second); err != nil {
		t.Fatalf("SaveRotationStatus() second: %v", err)
	}

	statuses, err := testStore.LoadRotationStatuses(ctx)
	if err != nil {
		t.Fatalf("LoadRotationStatuses(): %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("LoadRotationStatuses() = %+v, want exactly 1 row (upsert must replace)", statuses)
	}
	got := statuses[0]
	if got.Stage != second.Stage || got.Epoch != second.Epoch || got.Completed != second.Completed || got.CreateError != second.CreateError {
		t.Fatalf("LoadRotationStatuses()[0] = %+v, want replaced fields from %+v", got, second)
	}
	if !got.UpdatedAt.Equal(second.UpdatedAt) {
		t.Fatalf("UpdatedAt = %v, want %v", got.UpdatedAt, second.UpdatedAt)
	}
}

// Test flow:
//  1. Save a `store.RotationStatus` row for role "temp" and another for role "regular", both under the same model.
//  2. Assert `LoadRotationStatuses` returns both as distinct rows.
func rotationStatusDistinguishesRoleWithinSameModel(t *testing.T, open Opener) {
	testStore := open()
	ctx := context.Background()

	temp := store.RotationStatus{Model: "model-a", Role: "temp", Stage: "prepare_temp", UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	regular := store.RotationStatus{Model: "model-a", Role: "regular", Stage: "finish_regular", UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	if err := testStore.SaveRotationStatus(ctx, temp); err != nil {
		t.Fatalf("SaveRotationStatus(temp): %v", err)
	}
	if err := testStore.SaveRotationStatus(ctx, regular); err != nil {
		t.Fatalf("SaveRotationStatus(regular): %v", err)
	}

	statuses, err := testStore.LoadRotationStatuses(ctx)
	if err != nil {
		t.Fatalf("LoadRotationStatuses(): %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("LoadRotationStatuses() = %+v, want 2 distinct rows (same model, different role)", statuses)
	}
}

// Test flow:
//  1. Save three `store.RotationStatus` rows across two models and mixed roles, in an arbitrary insertion order.
//  2. Assert `LoadRotationStatuses` returns them sorted by model then role.
func loadRotationStatusesDeterministicOrder(t *testing.T, open Opener) {
	testStore := open()
	ctx := context.Background()

	updatedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := []store.RotationStatus{
		{Model: "model-b", Role: "regular", Stage: "s", UpdatedAt: updatedAt},
		{Model: "model-a", Role: "temp", Stage: "s", UpdatedAt: updatedAt},
		{Model: "model-a", Role: "regular", Stage: "s", UpdatedAt: updatedAt},
	}
	for _, row := range rows {
		if err := testStore.SaveRotationStatus(ctx, row); err != nil {
			t.Fatalf("SaveRotationStatus(%s/%s): %v", row.Model, row.Role, err)
		}
	}

	loaded, err := testStore.LoadRotationStatuses(ctx)
	if err != nil {
		t.Fatalf("LoadRotationStatuses(): %v", err)
	}
	wantOrder := []string{"model-a/regular", "model-a/temp", "model-b/regular"}
	if len(loaded) != len(wantOrder) {
		t.Fatalf("LoadRotationStatuses() = %d rows, want %d", len(loaded), len(wantOrder))
	}
	for i, want := range wantOrder {
		if got := loaded[i].Model + "/" + string(loaded[i].Role); got != want {
			t.Fatalf("LoadRotationStatuses()[%d] = %q, want %q (full order %+v)", i, got, want, loaded)
		}
	}
}

// Test flow:
//  1. Open a store, add three suspicious hosts, then remove one.
//  2. Close and reopen the store on the same database.
//  3. Assert `ListSuspiciousHosts` returns only the two hosts still pinned, sorted.
func suspiciousHostsSurviveAReopen(t *testing.T, open Opener) {
	first := open()
	ctx := context.Background()
	for _, participantKey := range []string{"validator-b", "validator-a", "validator-c"} {
		if err := first.AddSuspiciousHost(ctx, participantKey); err != nil {
			t.Fatalf("AddSuspiciousHost(%q): %v", participantKey, err)
		}
	}
	if err := first.RemoveSuspiciousHost(ctx, "validator-c"); err != nil {
		t.Fatalf("RemoveSuspiciousHost(): %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	second := open()

	pinned, err := second.ListSuspiciousHosts(ctx)
	if err != nil {
		t.Fatalf("ListSuspiciousHosts(): %v", err)
	}
	if want := []string{"validator-a", "validator-b"}; !slices.Equal(pinned, want) {
		t.Fatalf("ListSuspiciousHosts() = %v, want %v", pinned, want)
	}
}

// Test flow:
//  1. Open a test store.
//  2. Add the same host twice and remove a host that was never pinned.
//  3. Assert `ListSuspiciousHosts` returns the host once, unaffected by the repeat or the unknown removal.
func suspiciousHostsToleratesRepeatedPinsAndUnknownUnpins(t *testing.T, open Opener) {
	testStore := open()
	ctx := context.Background()

	if err := testStore.AddSuspiciousHost(ctx, "validator-a"); err != nil {
		t.Fatalf("first AddSuspiciousHost(): %v", err)
	}
	if err := testStore.AddSuspiciousHost(ctx, "validator-a"); err != nil {
		t.Fatalf("repeated AddSuspiciousHost(): %v", err)
	}
	if err := testStore.RemoveSuspiciousHost(ctx, "never-pinned"); err != nil {
		t.Fatalf("RemoveSuspiciousHost() for an unpinned host: %v", err)
	}

	pinned, err := testStore.ListSuspiciousHosts(ctx)
	if err != nil {
		t.Fatalf("ListSuspiciousHosts(): %v", err)
	}
	if want := []string{"validator-a"}; !slices.Equal(pinned, want) {
		t.Fatalf("ListSuspiciousHosts() = %v, want %v", pinned, want)
	}
}

// Test flow:
//  1. Save and replace the admin overrides, a rotation status, a model filter and a suspicious host.
//  2. Assert each read returns the replaced value, and that a second suspicious-host add is a no-op.
//  3. Remove the filter and the host and assert both lists are empty.
func smallTables(t *testing.T, open Opener) {
	gatewayStore := open()
	ctx := context.Background()
	for _, tokens := range []int64{1234, 999} {
		if err := gatewayStore.SaveOverrides(ctx, config.Overrides{Tunables: env.Tunables{DefaultMaxTokens: &tokens}}); err != nil {
			t.Fatalf("SaveOverrides(%d): %v", tokens, err)
		}
	}
	overrides, err := gatewayStore.LoadOverrides(ctx)
	if err != nil || overrides.DefaultMaxTokens == nil || *overrides.DefaultMaxTokens != 999 {
		t.Fatalf("LoadOverrides() = %+v, %v, want DefaultMaxTokens 999", overrides, err)
	}

	status := store.RotationStatus{Model: "qwen", Role: store.RoleRegular, Stage: "create", Epoch: 3, UpdatedAt: time.Unix(1_700_000_000, 0).UTC()}
	for _, completed := range []bool{false, true} {
		status.Completed = completed
		if err := gatewayStore.SaveRotationStatus(ctx, status); err != nil {
			t.Fatalf("SaveRotationStatus(): %v", err)
		}
	}
	statuses, err := gatewayStore.LoadRotationStatuses(ctx)
	if err != nil || !slices.Equal(statuses, []store.RotationStatus{status}) {
		t.Fatalf("LoadRotationStatuses() = %+v, %v; want %+v", statuses, err, status)
	}

	filter := store.ModelFilterRow{Model: "qwen", Preset: "strict", FiltersJSON: `{"a":1}`, UpdatedAt: "2026-01-01T00:00:00Z"}
	if err := gatewayStore.UpsertModelFilter(ctx, filter); err != nil {
		t.Fatalf("UpsertModelFilter(): %v", err)
	}
	filter.Preset = "loose"
	if err := gatewayStore.UpsertModelFilter(ctx, filter); err != nil {
		t.Fatalf("UpsertModelFilter(again): %v", err)
	}
	filters, err := gatewayStore.ListModelFilters(ctx)
	if err != nil || !slices.Equal(filters, []store.ModelFilterRow{filter}) {
		t.Fatalf("ListModelFilters() = %+v, %v; want %+v", filters, err, filter)
	}

	for range 2 {
		if err := gatewayStore.AddSuspiciousHost(ctx, "host-1"); err != nil {
			t.Fatalf("AddSuspiciousHost(): %v", err)
		}
	}
	hosts, err := gatewayStore.ListSuspiciousHosts(ctx)
	if err != nil || !slices.Equal(hosts, []string{"host-1"}) {
		t.Fatalf("ListSuspiciousHosts() = %v, %v; want [host-1]", hosts, err)
	}

	if err := gatewayStore.DeleteModelFilter(ctx, "qwen"); err != nil {
		t.Fatalf("DeleteModelFilter(): %v", err)
	}
	if err := gatewayStore.RemoveSuspiciousHost(ctx, "host-1"); err != nil {
		t.Fatalf("RemoveSuspiciousHost(): %v", err)
	}
	filters, _ = gatewayStore.ListModelFilters(ctx)
	hosts, _ = gatewayStore.ListSuspiciousHosts(ctx)
	if len(filters) != 0 || len(hosts) != 0 {
		t.Fatalf("after removal filters = %+v, hosts = %v, want none", filters, hosts)
	}
}

// Test flow:
//  1. Pin two hosts whose keys differ only by case, the lower-case one first.
//  2. Assert `ListSuspiciousHosts` returns the upper-case key first, the byte order, whatever collation the database defaults to.
func suspiciousHostsListInByteOrder(t *testing.T, open Opener) {
	testStore := open()
	ctx := context.Background()
	for _, key := range []string{"validator-a", "validator-B"} {
		if err := testStore.AddSuspiciousHost(ctx, key); err != nil {
			t.Fatalf("AddSuspiciousHost(%s): %v", key, err)
		}
	}
	pinned, err := testStore.ListSuspiciousHosts(ctx)
	if err != nil {
		t.Fatalf("ListSuspiciousHosts(): %v", err)
	}
	if want := []string{"validator-B", "validator-a"}; !slices.Equal(pinned, want) {
		t.Fatalf("ListSuspiciousHosts() = %v, want %v", pinned, want)
	}
}
