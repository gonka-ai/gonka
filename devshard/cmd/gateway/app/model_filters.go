package app

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	"devshard/cmd/gateway/api"
	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/internal/strictjson"
	"devshard/cmd/gateway/store"
	"devshard/logging"
)

type modelFilters struct {
	rows modelFilterStore
	now  func() time.Time

	writes  sync.Mutex
	current atomic.Pointer[modelFilterState]
}

type modelFilterStore interface {
	ListModelFilters(ctx context.Context) ([]store.ModelFilterRow, error)
	UpsertModelFilter(ctx context.Context, row store.ModelFilterRow) error
	DeleteModelFilter(ctx context.Context, model string) error
}

type modelFilterState struct {
	records  map[string]api.ModelFilterRecord
	byModel  map[string]*filters.Profile
	profiles *filters.ProfileSet
}

func newModelFilters(ctx context.Context, rows modelFilterStore, now func() time.Time) (*modelFilters, error) {
	stored, err := rows.ListModelFilters(ctx)
	if err != nil {
		return nil, err
	}
	records := make(map[string]api.ModelFilterRecord, len(stored))
	byModel := make(map[string]*filters.Profile, len(stored))
	for _, row := range stored {
		record, profile, err := readModelFilterRow(row)
		if err != nil {
			logging.Warn("a stored model filter setting is unreadable and skipped; the model runs its built-in profile",
				"model", row.Model, "error", err)
			records[row.Model] = api.ModelFilterRecord{
				Setting: filters.ModelSetting{Preset: filters.PresetName(row.Preset)}, UpdatedAt: row.UpdatedAt, Problem: err.Error(),
			}
			continue
		}
		records[row.Model] = record
		byModel[row.Model] = profile
	}
	registry := &modelFilters{rows: rows, now: now}
	registry.current.Store(newModelFilterState(records, byModel))
	return registry, nil
}

func readModelFilterRow(row store.ModelFilterRow) (api.ModelFilterRecord, *filters.Profile, error) {
	setting := filters.ModelSetting{Preset: filters.PresetName(row.Preset)}
	if err := strictjson.DecodeKnownFields([]byte(row.FiltersJSON), &setting.Filters); err != nil {
		return api.ModelFilterRecord{}, nil, fmt.Errorf("decoding filters: %w", err)
	}
	profile, err := filters.ComposeProfile(row.Model, setting)
	if err != nil {
		return api.ModelFilterRecord{}, nil, err
	}
	return api.ModelFilterRecord{Setting: setting, UpdatedAt: row.UpdatedAt}, profile, nil
}

func newModelFilterState(records map[string]api.ModelFilterRecord, byModel map[string]*filters.Profile) *modelFilterState {
	return &modelFilterState{records: records, byModel: byModel, profiles: filters.NewProfileSet(byModel)}
}

func (m *modelFilters) Profiles() *filters.ProfileSet {
	return m.current.Load().profiles
}

func (m *modelFilters) Snapshot() (map[string]api.ModelFilterRecord, *filters.ProfileSet) {
	state := m.current.Load()
	return state.records, state.profiles
}

func (m *modelFilters) Put(ctx context.Context, model string, setting filters.ModelSetting) error {
	profile, err := filters.ComposeProfile(model, setting)
	if err != nil {
		return err
	}
	encodedFilters, err := json.Marshal(setting.Filters)
	if err != nil {
		return fmt.Errorf("encoding filters for %s: %w", model, err)
	}
	m.writes.Lock()
	defer m.writes.Unlock()
	record := api.ModelFilterRecord{Setting: setting, UpdatedAt: m.now().UTC().Format(time.RFC3339)}
	row := store.ModelFilterRow{Model: model, Preset: string(setting.Preset), FiltersJSON: string(encodedFilters), UpdatedAt: record.UpdatedAt}
	if err := m.rows.UpsertModelFilter(ctx, row); err != nil {
		return err
	}
	previous := m.current.Load()
	records, byModel := maps.Clone(previous.records), maps.Clone(previous.byModel)
	records[model], byModel[model] = record, profile
	m.current.Store(newModelFilterState(records, byModel))
	return nil
}

func (m *modelFilters) Delete(ctx context.Context, model string) error {
	m.writes.Lock()
	defer m.writes.Unlock()
	if err := m.rows.DeleteModelFilter(ctx, model); err != nil {
		return err
	}
	previous := m.current.Load()
	records, byModel := maps.Clone(previous.records), maps.Clone(previous.byModel)
	delete(records, model)
	delete(byModel, model)
	m.current.Store(newModelFilterState(records, byModel))
	return nil
}
