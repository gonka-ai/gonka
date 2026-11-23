package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"devshard/cmd/gateway/env"
)

const rotationOfTwoModels = `[{"model_id":"model-a"},{"model_id":"model-b"}]`

func buildWithModels(t *testing.T, environment *string, overrides Overrides) (*Config, error) {
	t.Helper()
	rotation := rotationOfTwoModels
	if overrides.RotationModelsJSON == nil {
		overrides.RotationModelsJSON = &rotation
	}
	return Build(env.Values{ModelsJSON: environment}, overrides)
}

// Test flow:
//  1. For every field of `ModelSettings`, set only that field to two different values and apply each to a copy of the defaults.
//  2. Install the second copy as model-a's effective config under a network config.
//  3. Assert the two copies differ, `For(model-a)` returns the copy, and `For(model-b)` and the network config equal the defaults: a field with no line in `applyModelSettings` or one that aliases the network fails here.
func TestEveryModelSettingChangesOnlyItsModel(t *testing.T) {
	settingsType := reflect.TypeFor[ModelSettings]()
	for index := range settingsType.NumField() {
		field := settingsType.Field(index)
		t.Run(field.Name, func(t *testing.T) {
			first, second := Defaults(), Defaults()
			applyModelSettings(&first, modelSettingsWith(t, index, field.Type, 0))
			applyModelSettings(&second, modelSettingsWith(t, index, field.Type, 1))
			if reflect.DeepEqual(first, second) {
				t.Fatalf("%s reached no config field", field.Name)
			}

			network := Defaults()
			network.models = map[string]*Config{"model-a": &second}
			baseline := Defaults()
			if network.For("model-a") != &second {
				t.Fatalf("For(model-a) is not the model's own config")
			}
			if network.For("model-b") != &network {
				t.Errorf("For(model-b) is not the network config")
			}
			network.models = nil
			if !reflect.DeepEqual(network, baseline) {
				t.Errorf("setting %s on model-a changed the network config", field.Name)
			}
		})
	}
}

func modelSettingsWith(t *testing.T, index int, pointerType reflect.Type, choice int) ModelSettings {
	t.Helper()
	value := reflect.New(pointerType.Elem())
	switch value.Elem().Kind() {
	case reflect.Int64:
		value.Elem().SetInt(int64(choice + 1))
	case reflect.Float64:
		value.Elem().SetFloat(float64(choice + 1))
	case reflect.String:
		value.Elem().SetString(string(rune('a' + choice)))
	case reflect.Bool:
		value.Elem().SetBool(choice == 1)
	case reflect.Struct:
		lists := value.Interface().(*ParticipantOverrides)
		lists.Shadow = &[]string{"gonka1shadow" + string(rune('a'+choice))}
	default:
		t.Fatalf("no value for a setting of kind %s", value.Elem().Kind())
	}
	var settings ModelSettings
	reflect.ValueOf(&settings).Elem().Field(index).Set(value)
	return settings
}

// Test flow:
//  1. Collect every json key of `Overrides`, the embedded tunables included.
//  2. Walk every json key of `ModelSettings`.
//  3. Assert each is a network key, but for `max_model_len`, which only a model has.
func TestEveryModelSettingKeyIsANetworkKey(t *testing.T) {
	network := map[string]bool{}
	var collect func(reflect.Type)
	collect = func(structType reflect.Type) {
		for index := range structType.NumField() {
			field := structType.Field(index)
			if field.Anonymous {
				collect(field.Type)
				continue
			}
			name, _, _ := strings.Cut(field.Tag.Get("json"), jsonTagSeparator)
			network[name] = true
		}
	}
	collect(reflect.TypeFor[Overrides]())

	settingsType := reflect.TypeFor[ModelSettings]()
	for index := range settingsType.NumField() {
		key, _, _ := strings.Cut(settingsType.Field(index).Tag.Get("json"), jsonTagSeparator)
		if key != "max_model_len" && !network[key] {
			t.Errorf("model setting %q is no network key", key)
		}
	}
}

// Test flow:
//  1. Build with a model that raises host_cutoff_max_ms past the network's perf_ejection_max_seconds, one that lowers its own perf_ejection_max_seconds under the network's cutoff, and one that raises both together.
//  2. Assert the first two are refused with the `models[<id>].` prefix and the third is accepted: the cross-field rule holds per model, not on the network alone.
func TestACrossFieldRuleIsCheckedPerModel(t *testing.T) {
	cutoffAbove, perfBelow, perfAbove, cutoffRaised := int64(61_000), int64(30), int64(120), int64(100_000)
	refused := map[string]ModelSettings{
		"model-a": {HostCutoffMaxMS: &cutoffAbove},
		"model-b": {PerfEjectionMaxSeconds: &perfBelow},
	}
	for model, settings := range refused {
		_, err := buildWithModels(t, nil, Overrides{Models: map[string]ModelSettings{model: settings}})
		if err == nil || !strings.Contains(err.Error(), "models["+model+"].host_cutoff_max_ms") {
			t.Errorf("Build(%s) = %v, want a refusal prefixed models[%s].host_cutoff_max_ms", model, err, model)
		}
	}
	if _, err := buildWithModels(t, nil, Overrides{Models: map[string]ModelSettings{"model-a": {PerfEjectionMaxSeconds: &perfAbove, HostCutoffMaxMS: &cutoffRaised}}}); err != nil {
		t.Errorf("Build(a model raising both) = %v, want nil", err)
	}
}

// Test flow:
//  1. Build with GATEWAY_MODELS_JSON naming two settings of model-a and an admin layer that overrides one of them.
//  2. Assert the admin value wins for that field and the environment value stands for the other.
//  3. Build with an environment key that is no model setting, and with one that is a process-wide key, and assert both are refused.
func TestTheEnvironmentModelsLayerLosesToAdminPerField(t *testing.T) {
	environment := `{"model-a":{"max_burns_per_request":4,"match_wait_ms":100}}`
	adminWait := int64(300)

	built, err := buildWithModels(t, &environment, Overrides{Models: map[string]ModelSettings{"model-a": {MatchWaitMS: &adminWait}}})
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	if got := built.For("model-a").Scheduler; got.MaxBurnsPerRequest != 4 || got.MatchWaitMS != 300 {
		t.Errorf("model-a scheduler = burns %d wait %d, want 4 from the environment and 300 from admin", got.MaxBurnsPerRequest, got.MatchWaitMS)
	}
	if built.Scheduler.MaxBurnsPerRequest != Defaults().Scheduler.MaxBurnsPerRequest {
		t.Errorf("network max_burns_per_request = %d, want the default untouched", built.Scheduler.MaxBurnsPerRequest)
	}
	for _, bad := range []string{`{"model-a":{"no_such_key":1}}`, `{"model-a":{"rotation_enabled":true}}`, `not json`} {
		if _, err := buildWithModels(t, &bad, Overrides{}); err == nil {
			t.Errorf("Build with GATEWAY_MODELS_JSON %s = nil, want a refusal", bad)
		}
	}
}

// Test flow:
//  1. Build with a rotation list naming model-a and model-b and an environment models block for a third id.
//  2. Assert the build is refused and names the id.
//  3. Build with no rotation list and the same block, and assert the build passes: no list is visible to refuse it by.
func TestAModelNoListNamesIsRefusedOnlyWhenAListIsVisible(t *testing.T) {
	environment := `{"model-typo":{"match_wait_ms":100}}`
	overrides := Overrides{}
	if _, err := buildWithModels(t, &environment, overrides); err == nil || !strings.Contains(err.Error(), "models[model-typo]") {
		t.Errorf("Build() = %v, want a refusal naming models[model-typo]", err)
	}
	built, err := Build(env.Values{ModelsJSON: &environment}, overrides)
	if err != nil {
		t.Fatalf("Build() with no rotation list = %v, want nil", err)
	}
	if built.For("model-typo").Scheduler.MatchWaitMS != 100 {
		t.Errorf("model-typo match_wait_ms = %d, want 100", built.For("model-typo").Scheduler.MatchWaitMS)
	}
}

// Test flow:
//  1. Build with a network access of open and one model that sets its own access.
//  2. Assert an unnamed model resolves to the network access, never to a nil config, and the named model to its own.
func TestForAnUnnamedModelIsTheNetworkConfig(t *testing.T) {
	open, keyed := ModelAccessOpen, ModelAccessAPIKey
	built, err := Build(env.Values{}, Overrides{Access: &open, Models: map[string]ModelSettings{"model-a": {Access: &keyed}}}, "a-client-key-of-reasonable-length")
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	if built.For("unnamed") != built || built.For("unnamed").Limits.Access != ModelAccessOpen {
		t.Errorf("For(unnamed) is not the network config with access open")
	}
	if built.For("model-a").Limits.Access != ModelAccessAPIKey {
		t.Errorf("For(model-a).Limits.Access = %q, want api_key", built.For("model-a").Limits.Access)
	}
}

// Test flow:
//  1. Store two models, each with two settings, and merge a patch naming one setting of model-a.
//  2. Assert model-a keeps its other setting and takes the new value, and model-b is untouched.
//  3. Merge a null for one setting of model-a and assert only that setting clears.
//  4. Merge a null for model-b and assert its block is dropped and model-a stays.
//  5. Merge a patch with no `models` key and assert both stored blocks stay.
func TestMergingModelsWorksPerModelAndPerField(t *testing.T) {
	burns, wait, otherBurns, otherWait := int64(3), int64(100), int64(5), int64(200)
	stored := Overrides{Models: map[string]ModelSettings{
		"model-a": {MaxBurnsPerRequest: &burns, MatchWaitMS: &wait},
		"model-b": {MaxBurnsPerRequest: &otherBurns, MatchWaitMS: &otherWait},
	}}

	merged, err := stored.Merged([]byte(`{"models":{"model-a":{"match_wait_ms":250}}}`))
	if err != nil {
		t.Fatalf("Merged() = %v", err)
	}
	if firstModel := merged.Models["model-a"]; *firstModel.MaxBurnsPerRequest != 3 || *firstModel.MatchWaitMS != 250 {
		t.Errorf("model-a = burns %d wait %d, want 3 and 250", *firstModel.MaxBurnsPerRequest, *firstModel.MatchWaitMS)
	}
	if secondModel := merged.Models["model-b"]; *secondModel.MaxBurnsPerRequest != 5 || *secondModel.MatchWaitMS != 200 {
		t.Errorf("model-b = burns %d wait %d, want untouched 5 and 200", *secondModel.MaxBurnsPerRequest, *secondModel.MatchWaitMS)
	}
	if *stored.Models["model-a"].MatchWaitMS != 100 {
		t.Errorf("the stored overrides changed under a merge")
	}

	cleared, err := merged.Merged([]byte(`{"models":{"model-a":{"max_burns_per_request":null}}}`))
	if err != nil {
		t.Fatalf("Merged() with a null field = %v", err)
	}
	if firstModel := cleared.Models["model-a"]; firstModel.MaxBurnsPerRequest != nil || firstModel.MatchWaitMS == nil {
		t.Errorf("model-a after a null field = %+v, want only max_burns_per_request cleared", firstModel)
	}

	dropped, err := merged.Merged([]byte(`{"models":{"model-b":null}}`))
	if err != nil {
		t.Fatalf("Merged() with a null model = %v", err)
	}
	if _, kept := dropped.Models["model-b"]; kept {
		t.Error("model-b survived a null block")
	}
	if _, kept := dropped.Models["model-a"]; !kept {
		t.Error("model-a was dropped by a null for model-b")
	}

	untouched, err := merged.Merged([]byte(`{"match_wait_ms":500}`))
	if err != nil {
		t.Fatalf("Merged() without models = %v", err)
	}
	if len(untouched.Models) != 2 {
		t.Errorf("models after a patch that does not name them = %v, want both blocks", untouched.Models)
	}
}

// Test flow:
//  1. Merge patches that name a process-wide key, an unknown key and a misspelt key under a model.
//  2. Assert each is refused: only the keys of `ModelSettings` may appear under `models.<id>`.
func TestMergingRefusesAKeyNoModelHas(t *testing.T) {
	for _, patch := range []string{
		`{"models":{"model-a":{"disabled":true}}}`,
		`{"models":{"model-a":{"rotation_enabled":true}}}`,
		`{"models":{"model-a":{"max_burns_per_requests":1}}}`,
	} {
		if _, err := (Overrides{}).Merged([]byte(patch)); err == nil {
			t.Errorf("Merged(%s) = nil, want a refusal", patch)
		}
	}
}

// Test flow:
//  1. Build with a model block in the environment and a stored admin block for the same model, then merge a null for the admin block.
//  2. Rebuild from the merged overrides.
//  3. Assert the environment block still applies: null drops the admin layer's block only.
func TestANullModelDropsTheAdminBlockOnlyNotTheEnvironmentOne(t *testing.T) {
	environment := `{"model-a":{"max_burns_per_request":4}}`
	wait := int64(300)
	stored := Overrides{Models: map[string]ModelSettings{"model-a": {MatchWaitMS: &wait}}}

	merged, err := stored.Merged([]byte(`{"models":{"model-a":null}}`))
	if err != nil {
		t.Fatalf("Merged() = %v", err)
	}
	built, err := buildWithModels(t, &environment, merged)
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	if got := built.For("model-a").Scheduler; got.MaxBurnsPerRequest != 4 || got.MatchWaitMS != Defaults().Scheduler.MatchWaitMS {
		t.Errorf("model-a scheduler = burns %d wait %d, want the environment's 4 and the network wait", got.MaxBurnsPerRequest, got.MatchWaitMS)
	}
}

// Test flow:
//  1. Build with a rotation list naming model-a and a stored block for model-gone that marks it `api_key`, with no client key.
//  2. Assert the build passes and `Offers(model-gone)` is false.
//  3. Assert `CheckStoredModels` is an `ErrInvalid` naming model-gone and its null fix.
//  4. Build the same block with model-gone named by the environment and assert the build is refused.
//  5. Build with no stale block and assert `CheckStoredModels` is nil.
func TestAStoredBlockForAModelLeftOutOfRotationBootsButIsRefusedOnWrite(t *testing.T) {
	keyed := ModelAccessAPIKey
	rotation := `[{"model_id":"model-a"}]`
	stale := Overrides{Models: map[string]ModelSettings{"model-gone": {Access: &keyed}}}
	stale.RotationModelsJSON = &rotation
	built, err := Build(env.Values{}, stale)
	if err != nil {
		t.Fatalf("Build() with a stale stored block = %v, want nil", err)
	}
	if built.Offers("model-gone") {
		t.Errorf("Offers(model-gone) = true, want a stale block left out")
	}
	if err := built.CheckStoredModels(); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "model-gone") {
		t.Errorf("CheckStoredModels() = %v, want ErrInvalid naming model-gone", err)
	}

	environment := `{"model-gone":{"access":"api_key"}}`
	if _, err := Build(env.Values{ModelsJSON: &environment}, stale); err == nil || !strings.Contains(err.Error(), "models[model-gone]") {
		t.Errorf("Build() with the id in the environment = %v, want a refusal naming models[model-gone]", err)
	}

	cleanOverrides := Overrides{}
	cleanOverrides.RotationModelsJSON = &rotation
	clean, err := Build(env.Values{}, cleanOverrides)
	if err != nil || clean.CheckStoredModels() != nil {
		t.Errorf("Build() with no stale block = %v, CheckStoredModels = %v, want nil", err, clean.CheckStoredModels())
	}
}
