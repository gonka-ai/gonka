package config

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"devshard/cmd/gateway/env"
	"devshard/cmd/gateway/internal/logcapture"
)

const (
	legacyModel        = "model-a"
	legacyStoredLimits = `{"model_limits":{"model-a":{"default_max_tokens":100,"max_tokens_cap":200,"max_model_len":3000,"max_concurrent_requests":7,"max_input_tokens_in_flight":9000}},"model_access":{"model-a":"open"}}`
	fakeEnvKeyAlpha    = "fake-env-key-alpha-0001"
	fakeEnvKeyBravo    = "fake-env-key-bravo-0002"
	fakeStoredKey      = "fake-stored-key-0003"
)

func storedOverrides(t *testing.T, document string) Overrides {
	t.Helper()
	overrides, err := ParseStoredOverrides([]byte(document))
	if err != nil {
		t.Fatalf("ParseStoredOverrides(%s) = %v", document, err)
	}
	return overrides
}

// Test flow:
//  1. Build from a stored document holding `model_limits` and `model_access` for one model, and from one holding the same values under `models`.
//  2. Assert both give the model the same limits and access, and the other model keeps the network values.
//  3. Encode the legacy document and assert it keeps both legacy keys and gains no `models`.
func TestALegacyStoredDocumentBuildsTheValuesModelsWouldGive(t *testing.T) {
	legacy, err := buildWithModels(t, nil, storedOverrides(t, legacyStoredLimits))
	if err != nil {
		t.Fatalf("Build(legacy) = %v", err)
	}
	current, err := buildWithModels(t, nil, storedOverrides(t, `{"models":{"model-a":{"default_max_tokens":100,"max_tokens_cap":200,"max_model_len":3000,"max_concurrent_requests":7,"max_input_tokens_in_flight":9000,"access":"open"}}}`))
	if err != nil {
		t.Fatalf("Build(models) = %v", err)
	}

	got, want := legacy.For(legacyModel).Limits, current.For(legacyModel).Limits
	if got.DefaultMaxTokens != 100 || got.MaxTokensCap != 200 || got.Access != ModelAccessOpen || got.Concurrency.MaxRequests != 7 || got.MaxInputTokensInFlight != 9000 || got.MaxModelLen == nil || *got.MaxModelLen != 3000 {
		t.Fatalf("legacy limits = %+v, want the stored values", got)
	}
	if got.DefaultMaxTokens != want.DefaultMaxTokens || got.Access != want.Access || got.Concurrency != want.Concurrency || *got.MaxModelLen != *want.MaxModelLen {
		t.Fatalf("legacy limits = %+v, models limits = %+v, want equal", got, want)
	}
	if !legacy.Offers(legacyModel) || legacy.Offers("model-b") || legacy.For("model-b").Limits.Access != legacy.Limits.Access {
		t.Fatalf("Offers/For: the legacy model must be offered and the other must keep the network values")
	}

	encoded, err := storedOverrides(t, legacyStoredLimits).EncodeOverrides()
	if err != nil || !strings.Contains(string(encoded), `"model_limits"`) || !strings.Contains(string(encoded), `"model_access"`) || strings.Contains(string(encoded), `"models"`) {
		t.Fatalf("EncodeOverrides() = %s, %v, want the document as stored", encoded, err)
	}
}

// Test flow:
//  1. Build from a document whose legacy keys set a model's limits and access and whose `models` block sets the cap and access of the same model.
//  2. Assert the fields `models` names are its values and the fields only the legacy keys name are theirs.
func TestModelsWinOverTheLegacyFormFieldByField(t *testing.T) {
	overrides := storedOverrides(t, `{"model_limits":{"model-a":{"default_max_tokens":100,"max_tokens_cap":200,"max_model_len":3000}},"model_access":{"model-a":"open"},"models":{"model-a":{"max_tokens_cap":500,"access":"admin_only"}}}`)

	built, err := buildWithModels(t, nil, overrides)
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}

	limits := built.For(legacyModel).Limits
	if limits.MaxTokensCap != 500 || limits.Access != ModelAccessAdminOnly {
		t.Fatalf("cap = %d, access = %s, want models.model-a to win with 500 and admin_only", limits.MaxTokensCap, limits.Access)
	}
	if limits.DefaultMaxTokens != 100 || limits.MaxModelLen == nil || *limits.MaxModelLen != 3000 {
		t.Fatalf("default = %d, max_model_len = %v, want the legacy values where models is silent", limits.DefaultMaxTokens, limits.MaxModelLen)
	}
}

// Test flow:
//  1. Build from a legacy `model_limits` entry naming no token pair, and from a legacy `model_access` value outside the three tiers.
//  2. Assert each is refused as an invalid configuration whose message carries the `models[model-a].` prefix.
func TestLegacySettingsAreValidatedAsModelsWithItsPrefix(t *testing.T) {
	for name, document := range map[string]string{
		"limits": `{"model_limits":{"model-a":{"max_model_len":3000}}}`,
		"access": `{"model_access":{"model-a":"everyone"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := buildWithModels(t, nil, storedOverrides(t, document))

			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "models[model-a].") {
				t.Fatalf("Build() = %v, want ErrInvalid naming models[model-a].", err)
			}
		})
	}
}

// Test flow:
//  1. Store a document holding `model_access` and `model_limits` and a `models` block.
//  2. Merge a patch naming `model_access` with one other model, then one setting `model_limits` to null.
//  3. Assert the first replaces the whole map as before, the second clears only `model_limits`, and `models` and the other legacy key stay.
func TestMergingTheLegacyKeysKeepsTheirWholeMapSemantics(t *testing.T) {
	stored := storedOverrides(t, `{"model_access":{"model-a":"open"},"model_limits":{"model-a":{"default_max_tokens":1,"max_tokens_cap":2}},"models":{"model-a":{"access":"api_key"}}}`)

	replaced, err := stored.Merged([]byte(`{"model_access":{"model-b":"api_key"}}`))
	if err != nil {
		t.Fatalf("Merged() = %v", err)
	}
	if len(replaced.ModelAccess) != 1 || replaced.ModelAccess["model-b"] != ModelAccessAPIKey || len(replaced.ModelLimits) != 1 || len(replaced.Models) != 1 {
		t.Fatalf("replaced = %+v, want the access map replaced and the rest kept", replaced)
	}
	cleared, err := replaced.Merged([]byte(`{"model_limits":null}`))
	if err != nil {
		t.Fatalf("Merged() with null = %v", err)
	}
	if cleared.ModelLimits != nil || len(cleared.ModelAccess) != 1 || len(cleared.Models) != 1 {
		t.Fatalf("cleared = %+v, want only model_limits cleared", cleared)
	}
}

// Test flow:
//  1. Build from a document whose legacy keys name a model that `rotation_models_json` does not list, and whose `models` block names another unlisted model.
//  2. Assert the legacy-only model is built with its values and offered, as it was before `models` existed, and `CheckStoredModels` passes.
//  3. Assert the model named by `models` alone is dropped as stale, so a write is refused until it is cleared.
func TestALegacyEntryForAModelOutsideTheRotationStillApplies(t *testing.T) {
	rotation := `[{"model_id":"model-b"}]`
	overrides := storedOverrides(t, `{"model_access":{"model-a":"open"},"models":{"model-gone":{"access":"open"}}}`)
	overrides.RotationModelsJSON = &rotation

	built, err := Build(env.Values{}, overrides)
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}

	if !built.Offers(legacyModel) || built.For(legacyModel).Limits.Access != ModelAccessOpen {
		t.Fatalf("legacy model-a: offered %v access %s, want it offered and open", built.Offers(legacyModel), built.For(legacyModel).Limits.Access)
	}
	if built.Offers("model-gone") {
		t.Fatalf("model-gone named by models alone is offered, want it dropped as stale")
	}
	if err := built.CheckStoredModels(); err == nil || !strings.Contains(err.Error(), "model-gone") || strings.Contains(err.Error(), legacyModel) {
		t.Fatalf("CheckStoredModels() = %v, want it to name only model-gone", err)
	}
}

// Test flow:
//  1. Log-capture a document holding both legacy keys, one holding only `models`, and one holding neither.
//  2. Call `WarnLegacyKeys` on each.
//  3. Assert the first logs one warning per key naming the key, its replacement and the removal, and the others log nothing.
func TestWarnLegacyKeysNamesEachLegacyKeyOnce(t *testing.T) {
	logged := logcapture.Install(t)

	storedOverrides(t, `{"models":{"model-a":{"access":"open"}}}`).WarnLegacyKeys()
	Overrides{}.WarnLegacyKeys()
	if len(logged.All()) != 0 {
		t.Fatalf("warnings = %+v, want none without a legacy key", logged.All())
	}
	storedOverrides(t, legacyStoredLimits).WarnLegacyKeys()

	var messages []string
	for _, entry := range logged.All() {
		if entry.Level == "warn" {
			messages = append(messages, entry.Msg)
		}
	}
	if len(messages) != 2 || !strings.Contains(messages[0], "model_limits") || !strings.Contains(messages[1], "model_access") {
		t.Fatalf("warnings = %q, want one for model_limits and one for model_access", messages)
	}
	for _, message := range messages {
		if !strings.Contains(message, "models.<id>") || !strings.Contains(message, "v7 or v8") {
			t.Fatalf("warning %q must name the replacement and the planned removal", message)
		}
	}
}

// Test flow:
//  1. Build with the environment keys `DEVSHARD_API_KEYS` as a comma list with blanks and one key stored in the database.
//  2. Assert the environment keys are trimmed and listed first, and the active keys are the environment ones then the stored one.
//  3. Build with a short environment key and assert it is refused as at the old parsing.
//  4. Build with an environment key that holds whitespace and one equal to the admin key, and assert both are accepted as at the old validation.
//  5. Build with only an environment key and a model on `api_key` and assert the last-key rule is satisfied.
func TestEnvironmentClientKeysAreParsedValidatedAndCounted(t *testing.T) {
	list := " " + fakeEnvKeyAlpha + " ,, " + fakeEnvKeyBravo + ","
	built, err := Build(env.Values{APIKeys: &list}, Overrides{}, fakeStoredKey)
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	if want := []string{fakeEnvKeyAlpha, fakeEnvKeyBravo}; !slices.Equal(built.Server.EnvAPIKeys, want) {
		t.Fatalf("EnvAPIKeys = %q, want %q", built.Server.EnvAPIKeys, want)
	}
	if want := []string{fakeEnvKeyAlpha, fakeEnvKeyBravo, fakeStoredKey}; !slices.Equal(built.Server.APIKeys, want) {
		t.Fatalf("APIKeys = %q, want %q", built.Server.APIKeys, want)
	}

	short := "too-short"
	if _, err := Build(env.Values{APIKeys: &short}, Overrides{}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "api_keys[0]: must be at least") {
		t.Fatalf("Build(short key) = %v, want the api_keys[0] length refusal", err)
	}

	admin := fakeEnvKeyAlpha
	spaced := "fake env key with spaces"
	lenient := fakeEnvKeyAlpha + "," + spaced
	if _, err := Build(env.Values{APIKeys: &lenient, AdminAPIKey: &admin}, Overrides{}); err != nil {
		t.Fatalf("Build(environment keys with whitespace and the admin key) = %v, want the old length-only validation", err)
	}

	onlyEnvironment := fakeEnvKeyAlpha
	access := ModelAccessAPIKey
	keyed, err := Build(env.Values{APIKeys: &onlyEnvironment}, Overrides{Access: &access})
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	if err := keyed.CheckClientKeys(); err != nil {
		t.Fatalf("CheckClientKeys() = %v, want an environment key to count as active", err)
	}
}
