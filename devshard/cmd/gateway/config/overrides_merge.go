package config

import (
	"fmt"
	"maps"
	"reflect"
	"strings"

	json "github.com/goccy/go-json"
)

const (
	jsonTagSeparator = ","
	modelsKey        = "models"
	jsonNull         = "null"
)

// Merged applies a settings patch: a field it names replaces the stored one, a field it sets to null clears it, and every other field stays; `models` merges per model and per field, and a model set to null is dropped. See ../docs/configuration.md.
func (o Overrides) Merged(patch []byte) (Overrides, error) {
	changes, err := ParseOverrides(patch)
	if err != nil {
		return Overrides{}, err
	}
	var named map[string]json.RawMessage
	if err := json.Unmarshal(patch, &named); err != nil {
		return Overrides{}, fmt.Errorf("parsing overrides patch: %w", err)
	}
	merged := o
	replaceNamedFields(reflect.ValueOf(&merged).Elem(), reflect.ValueOf(changes), named)
	if changes.Models != nil {
		merged.Models, err = mergedModels(o.Models, named[modelsKey])
		if err != nil {
			return Overrides{}, err
		}
	}
	return merged, nil
}

func mergedModels(stored map[string]ModelSettings, patch json.RawMessage) (map[string]ModelSettings, error) {
	var named map[string]json.RawMessage
	if err := json.Unmarshal(patch, &named); err != nil {
		return nil, fmt.Errorf("parsing models patch: %w", err)
	}
	merged := maps.Clone(stored)
	if merged == nil {
		merged = make(map[string]ModelSettings, len(named))
	}
	for model, raw := range named {
		if string(raw) == jsonNull {
			delete(merged, model)
			continue
		}
		var changes ModelSettings
		if err := json.Unmarshal(raw, &changes); err != nil {
			return nil, fmt.Errorf("parsing models[%s] patch: %w", model, err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, fmt.Errorf("parsing models[%s] patch: %w", model, err)
		}
		settings := merged[model]
		replaceNamedFields(reflect.ValueOf(&settings).Elem(), reflect.ValueOf(changes), fields)
		merged[model] = settings
	}
	return merged, nil
}

func replaceNamedFields(target, source reflect.Value, named map[string]json.RawMessage) {
	fields := target.Type()
	for index := range fields.NumField() {
		if fields.Field(index).Anonymous {
			replaceNamedFields(target.Field(index), source.Field(index), named)
			continue
		}
		name, _, _ := strings.Cut(fields.Field(index).Tag.Get("json"), jsonTagSeparator)
		if _, present := named[name]; present {
			target.Field(index).Set(source.Field(index))
		}
	}
}
