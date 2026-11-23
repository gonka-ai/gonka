package config

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	json "github.com/goccy/go-json"

	"devshard/cmd/gateway/internal/logkey"
	"devshard/logging"
)

func resolveModels(network *Config, modelsJSON *string, legacy, admin map[string]ModelSettings) error {
	fromEnvironment, err := parseModelsJSON(modelsJSON)
	if err != nil {
		return err
	}
	named := slices.Sorted(maps.Keys(fromEnvironment))
	named = append(named, slices.Sorted(maps.Keys(admin))...)
	slices.Sort(named)
	named = slices.Compact(named)
	identifiers := append(slices.Clone(named), slices.Sorted(maps.Keys(legacy))...)
	slices.Sort(identifiers)
	identifiers = slices.Compact(identifiers)
	if len(identifiers) == 0 {
		network.models = nil
		return nil
	}

	problems, stale := unknownModelProblems(named, fromEnvironment, network.Rotation.ModelsJSON)
	inherited := problemTexts(network.problems())
	effective := make(map[string]*Config, len(identifiers))
	for _, identifier := range identifiers {
		if slices.Contains(stale, identifier) {
			continue
		}
		if strings.TrimSpace(identifier) == "" {
			problems = append(problems, errors.New("models: a model id must not be blank"))
			continue
		}
		model := *network
		applyModelSettings(&model, fromEnvironment[identifier])
		applyModelSettings(&model, legacy[identifier])
		applyModelSettings(&model, admin[identifier])
		for _, problem := range model.problems() {
			if !inherited[problem.Error()] {
				problems = append(problems, fmt.Errorf("models[%s].%w", identifier, problem))
			}
		}
		effective[identifier] = &model
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %w", ErrInvalid, errors.Join(problems...))
	}
	network.models = effective
	network.staleModels = stale
	return nil
}

func problemTexts(problems []error) map[string]bool {
	texts := make(map[string]bool, len(problems))
	for _, problem := range problems {
		texts[problem.Error()] = true
	}
	return texts
}

func parseModelsJSON(raw *string) (map[string]ModelSettings, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(*raw)))
	decoder.DisallowUnknownFields()
	var models map[string]ModelSettings
	if err := decoder.Decode(&models); err != nil {
		return nil, fmt.Errorf("%w: parsing GATEWAY_MODELS_JSON: %w", ErrInvalid, err)
	}
	return models, nil
}

func unknownModelProblems(identifiers []string, fromEnvironment map[string]ModelSettings, rotationModelsJSON string) ([]error, []string) {
	known := rotationModelIDs(rotationModelsJSON)
	var problems []error
	var stale []string
	for _, identifier := range identifiers {
		_, named := fromEnvironment[identifier]
		switch {
		case len(known) == 0:
			logging.Warn("models names a model no list can confirm: neither rotation_models_json nor a chain snapshot is visible here",
				logkey.Subsystem, "config", logkey.Model, identifier)
		case slices.Contains(known, identifier):
		case named:
			problems = append(problems, fmt.Errorf("models[%s]: no model of that id in rotation_models_json", identifier))
		default:
			stale = append(stale, identifier)
			logging.Warn("a stored models block names a model rotation_models_json no longer lists",
				logkey.Subsystem, "config", logkey.Model, identifier)
		}
	}
	return problems, stale
}

func rotationModelIDs(raw string) []string {
	var entries []struct {
		ModelID string `json:"model_id"`
	}
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &entries) != nil {
		return nil
	}
	identifiers := make([]string, 0, len(entries))
	for _, entry := range entries {
		identifiers = append(identifiers, entry.ModelID)
	}
	return identifiers
}

// ResolveModels gives the models named in settings their effective configurations over this one, as Build does for the admin layer.
func (c *Config) ResolveModels(settings map[string]ModelSettings) error {
	return resolveModels(c, nil, nil, settings)
}
