package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"devshard/cmd/gateway/api"
	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/store"
)

const (
	filtersTestQwenModel = "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8"
	filtersTestKimiModel = "moonshotai/Kimi-K2.6"
	filtersTestGLMModel  = "zai-org/GLM-5.3-Flash"
)

type memoryModelFilterStore struct {
	mu      sync.Mutex
	rows    map[string]store.ModelFilterRow
	upserts int
}

func newMemoryModelFilterStore(rows ...store.ModelFilterRow) *memoryModelFilterStore {
	memory := &memoryModelFilterStore{rows: map[string]store.ModelFilterRow{}}
	for _, row := range rows {
		memory.rows[row.Model] = row
	}
	return memory
}

func (m *memoryModelFilterStore) ListModelFilters(context.Context) ([]store.ModelFilterRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	listed := make([]store.ModelFilterRow, 0, len(m.rows))
	for _, row := range m.rows {
		listed = append(listed, row)
	}
	return listed, nil
}

func (m *memoryModelFilterStore) UpsertModelFilter(_ context.Context, row store.ModelFilterRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.upserts++
	m.rows[row.Model] = row
	return nil
}

func (m *memoryModelFilterStore) DeleteModelFilter(_ context.Context, model string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, model)
	return nil
}

func fixedClock() time.Time { return time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC) }

func newTestModelFilters(t *testing.T, rows *memoryModelFilterStore) *modelFilters {
	t.Helper()
	registry, err := newModelFilters(context.Background(), rows, fixedClock)
	if err != nil {
		t.Fatalf("newModelFilters(): %v", err)
	}
	return registry
}

// Test flow:
//  1. Bind Qwen to minimax.
//  2. Assert the profile set the chat path reads strips thinking for Qwen at once, and the record carries the setting and its time.
func TestModelFiltersPutAppliesWithoutRestart(t *testing.T) {
	registry := newTestModelFilters(t, newMemoryModelFilterStore())

	if err := registry.Put(context.Background(), filtersTestQwenModel, filters.ModelSetting{Preset: presetMinimaxForTest}); err != nil {
		t.Fatalf("Put(): %v", err)
	}

	if got := registry.Profiles().For(filtersTestQwenModel).Thinking; got != filters.ThinkingStrip {
		t.Errorf("Qwen thinking = %v, want ThinkingStrip", got)
	}
	record := storedRecords(registry)[filtersTestQwenModel]
	if record.Setting.Preset != presetMinimaxForTest || record.UpdatedAt != "2026-10-19T12:00:00Z" {
		t.Errorf("record = %+v, want minimax at the clock's time", record)
	}
}

// Test flow:
//  1. Bind Kimi to default, then delete the setting.
//  2. Assert Kimi is back on its built-in profile, thinking budget included.
func TestModelFiltersDeleteRevertsToBuiltIn(t *testing.T) {
	registry := newTestModelFilters(t, newMemoryModelFilterStore())
	if err := registry.Put(context.Background(), filtersTestKimiModel, filters.ModelSetting{Preset: filters.PresetDefault}); err != nil {
		t.Fatalf("Put(): %v", err)
	}
	if registry.Profiles().For(filtersTestKimiModel).ThinkingTokenBudget != nil {
		t.Fatal("Kimi bound to default kept its thinking budget")
	}

	if err := registry.Delete(context.Background(), filtersTestKimiModel); err != nil {
		t.Fatalf("Delete(): %v", err)
	}

	if registry.Profiles().For(filtersTestKimiModel).ThinkingTokenBudget == nil {
		t.Error("Kimi did not return to its built-in profile")
	}
	if _, stored := storedRecords(registry)[filtersTestKimiModel]; stored {
		t.Error("the deleted record is still listed")
	}
}

// Test flow:
//  1. Put an output-token floor above the ceiling, which composing refuses.
//  2. Assert the refusal is an invalid setting and nothing reached the store.
func TestModelFiltersRefusedSettingNeverStored(t *testing.T) {
	rows := newMemoryModelFilterStore()
	registry := newTestModelFilters(t, rows)

	tooHigh := filters.MaxOutputTokensFloor + 1
	err := registry.Put(context.Background(), filtersTestQwenModel, filters.ModelSetting{Filters: filters.FilterKnobs{OutputTokensFloor: &tooHigh}})

	if !errors.Is(err, filters.ErrInvalidModelSetting) {
		t.Fatalf("Put() error = %v, want ErrInvalidModelSetting", err)
	}
	if rows.upserts != 0 {
		t.Errorf("store saw %d upserts, want none", rows.upserts)
	}
}

// Test flow:
//  1. Put settings for two different models from two goroutines at once.
//  2. Assert both survive in the records and in the profile set.
func TestModelFiltersConcurrentPutsBothSurvive(t *testing.T) {
	registry := newTestModelFilters(t, newMemoryModelFilterStore())
	var group sync.WaitGroup
	for _, model := range []string{filtersTestQwenModel, filtersTestGLMModel} {
		group.Go(func() {
			if err := registry.Put(context.Background(), model, filters.ModelSetting{Preset: presetMinimaxForTest}); err != nil {
				t.Errorf("Put(%q): %v", model, err)
			}
		})
	}
	group.Wait()

	for _, model := range []string{filtersTestQwenModel, filtersTestGLMModel} {
		if _, stored := storedRecords(registry)[model]; !stored {
			t.Errorf("%s lost its setting", model)
		}
		if registry.Profiles().For(model).Thinking != filters.ThinkingStrip {
			t.Errorf("%s does not run minimax", model)
		}
	}
}

// Test flow:
//  1. Boot from a store holding a row with a retired preset, a row with corrupt JSON, a row with a misspelt knob, and a valid row.
//  2. Assert the boot succeeds, the unreadable rows do not apply (each model keeps its built-in profile) but stay listed with their problem, and the valid row applies.
func TestModelFiltersBootSkipsUnreadableRows(t *testing.T) {
	rows := newMemoryModelFilterStore(
		store.ModelFilterRow{Model: filtersTestKimiModel, Preset: "retired", FiltersJSON: `{}`},
		store.ModelFilterRow{Model: filtersTestGLMModel, Preset: "default", FiltersJSON: `{`},
		store.ModelFilterRow{Model: "MiniMaxAI/MiniMax-M2.7", Preset: "default", FiltersJSON: `{"thinkng":"normalize"}`},
		store.ModelFilterRow{Model: filtersTestQwenModel, Preset: "minimax", FiltersJSON: `{"output_tokens_floor":32}`},
	)

	registry := newTestModelFilters(t, rows)

	if registry.Profiles().For(filtersTestKimiModel).ThinkingTokenBudget == nil {
		t.Error("Kimi with an unreadable row lost its built-in profile")
	}
	if registry.Profiles().For(filtersTestGLMModel).Thinking != filters.ThinkingForceOn {
		t.Error("GLM with corrupt JSON lost its built-in profile")
	}
	if got := registry.Profiles().For(filtersTestQwenModel); got.Thinking != filters.ThinkingStrip || got.OutputTokensFloor != 32 {
		t.Errorf("Qwen profile = %+v, want minimax with floor 32", got)
	}
	if registry.Profiles().For("MiniMaxAI/MiniMax-M2.7").Thinking != filters.ThinkingStrip {
		t.Error("MiniMax with a misspelt knob lost its built-in profile")
	}
	records := storedRecords(registry)
	if len(records) != 4 || records[filtersTestQwenModel].Problem != "" {
		t.Fatalf("records = %+v, want all four rows with the readable one clean", records)
	}
	for _, model := range []string{filtersTestKimiModel, filtersTestGLMModel, "MiniMaxAI/MiniMax-M2.7"} {
		if records[model].Problem == "" {
			t.Errorf("%s: unreadable row carries no problem", model)
		}
	}
}

func storedRecords(registry *modelFilters) map[string]api.ModelFilterRecord {
	records, _ := registry.Snapshot()
	return records
}
