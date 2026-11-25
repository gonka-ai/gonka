package filters

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"

	"devshard/cmd/gateway/internal/strictjson"
)

// MaxOutputTokensFloor bounds output_tokens_floor at the default output budget cap; the pattern and extension name the embedded preset files.
const (
	MaxOutputTokensFloor = RequestMaxTokensCap

	presetFilePattern   = "presets/*.json"
	presetFileExtension = ".json"
)

//go:embed presets/*.json
var embeddedPresetFiles embed.FS

var (
	ErrInvalidModelSetting = errors.New("invalid model filter setting")

	presetProfiles, builtInBindings = mustLoadPresets(embeddedPresetFiles)
	defaultProfile                  = presetProfiles[PresetDefault]
)

type presetFile struct {
	Models  []string    `json:"models"`
	Filters FilterKnobs `json:"filters"`
}

// ModelSetting is what an operator stores for one model: a preset, knobs over it, or both.
type ModelSetting struct {
	Preset  PresetName  `json:"preset,omitempty"`
	Filters FilterKnobs `json:"filters"`
}

// IsEmpty reports a setting that names neither a preset nor a knob.
func (setting ModelSetting) IsEmpty() bool {
	return setting.Preset == presetUnnamed && setting.Filters == FilterKnobs{}
}

// PresetFor is the preset the setting composes over for model: the named one, else the model's built-in preset, else default.
func (setting ModelSetting) PresetFor(model string) PresetName {
	if setting.Preset != presetUnnamed {
		return setting.Preset
	}
	if builtIn, bound := BuiltInPresetFor(model); bound {
		return builtIn
	}
	return PresetDefault
}

// PresetNames lists the embedded presets, sorted.
func PresetNames() []PresetName {
	return slices.Sorted(maps.Keys(presetProfiles))
}

// PresetCatalog is every preset written as a filters object, copies no caller can change the catalog through.
func PresetCatalog() map[PresetName]FilterKnobs {
	catalog := make(map[PresetName]FilterKnobs, len(presetProfiles))
	for name, profile := range presetProfiles {
		catalog[name] = profile.Knobs()
	}
	return catalog
}

// ComposeProfile resolves a setting into a fresh profile, refusing any knob value that loosens a request guard.
func ComposeProfile(model string, setting ModelSetting) (*Profile, error) {
	presetName := setting.PresetFor(model)
	preset, known := presetProfiles[presetName]
	if !known {
		return nil, fmt.Errorf("%w: unknown preset %q; known presets: %v", ErrInvalidModelSetting, presetName, PresetNames())
	}
	return composeOver(*preset, setting.Filters)
}

func mustLoadPresets(files fs.FS) (map[PresetName]*Profile, map[string]PresetName) {
	profiles, bindings, err := loadPresets(files)
	if err != nil {
		panic(err)
	}
	return profiles, bindings
}

func loadPresets(files fs.FS) (map[PresetName]*Profile, map[string]PresetName, error) {
	paths, err := fs.Glob(files, presetFilePattern)
	if err != nil {
		return nil, nil, fmt.Errorf("listing presets: %w", err)
	}
	presetFiles := make(map[PresetName]presetFile, len(paths))
	for _, filePath := range paths {
		file, err := readPresetFile(files, filePath)
		if err != nil {
			return nil, nil, err
		}
		presetFiles[PresetName(strings.TrimSuffix(path.Base(filePath), presetFileExtension))] = file
	}
	defaultFile, present := presetFiles[PresetDefault]
	if !present {
		return nil, nil, fmt.Errorf("presets: %s%s is missing", PresetDefault, presetFileExtension)
	}
	base, err := composeOver(Profile{}, defaultFile.Filters)
	if err != nil {
		return nil, nil, fmt.Errorf("preset %s: %w", PresetDefault, err)
	}
	profiles := make(map[PresetName]*Profile, len(presetFiles))
	bindings := map[string]PresetName{}
	for _, name := range slices.Sorted(maps.Keys(presetFiles)) {
		profile, err := composeOver(*base, presetFiles[name].Filters)
		if err != nil {
			return nil, nil, fmt.Errorf("preset %s: %w", name, err)
		}
		profiles[name] = profile
		for _, model := range presetFiles[name].Models {
			if earlier, bound := bindings[model]; bound {
				return nil, nil, fmt.Errorf("model %s is bound by both preset %s and preset %s", model, earlier, name)
			}
			bindings[model] = name
		}
	}
	return profiles, bindings, nil
}

func readPresetFile(files fs.FS, filePath string) (presetFile, error) {
	raw, err := fs.ReadFile(files, filePath)
	if err != nil {
		return presetFile{}, fmt.Errorf("reading preset %s: %w", filePath, err)
	}
	var file presetFile
	if err := strictjson.DecodeKnownFields(raw, &file); err != nil {
		return presetFile{}, fmt.Errorf("decoding preset %s: %w", filePath, err)
	}
	return file, nil
}
