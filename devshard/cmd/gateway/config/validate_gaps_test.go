package config

import (
	"errors"
	"strings"
	"testing"

	"devshard/cmd/gateway/env"
)

// Test flow:
//  1. For each case, start from the defaults, open one model to `api_key` callers with a valid client key, and apply the case's change.
//  2. The cases are a one-character client API key, a client key one character under `minAPIKeyLength` beside a valid one, a long client key with a space inside it that no `Bearer` header can carry, a client key equal to the admin key, a drain timeout of one second, and a drain timeout one second under `MinDrainTimeoutSeconds`.
//  3. Assert `Validate` refuses every case with an error naming the case's own field.
func TestValidateRefusesShortClientKeysAndADrainBelowTheRaceDeadlines(t *testing.T) {
	testCases := []struct {
		name            string
		apply           func(*Config)
		messageFragment string
	}{
		{name: "one-character client api key", apply: func(c *Config) { c.Server.APIKeys = []string{"k"} }, messageFragment: "api_keys"},
		{name: "client api key one under the minimum", apply: func(c *Config) {
			c.Server.APIKeys = append(c.Server.APIKeys, strings.Repeat("k", minAPIKeyLength-1))
		}, messageFragment: "api_keys"},
		{name: "client api key with an inner space", apply: func(c *Config) {
			c.Server.APIKeys = append(c.Server.APIKeys, "a client key with spaces")
		}, messageFragment: "whitespace"},
		{name: "client api key equal to the admin key", apply: func(c *Config) {
			c.Server.AdminAPIKey = "an-admin-key-of-reasonable-length"
			c.Server.APIKeys = append(c.Server.APIKeys, c.Server.AdminAPIKey)
		}, messageFragment: "differ from the admin key"},
		{name: "drain timeout of one second", apply: func(c *Config) { c.Stream.DrainTimeoutSeconds = 1 }, messageFragment: "drain_timeout_seconds"},
		{name: "drain timeout one under the race deadlines", apply: func(c *Config) { c.Stream.DrainTimeoutSeconds = MinDrainTimeoutSeconds - 1 }, messageFragment: "drain_timeout_seconds"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			configuration := Defaults()
			configuration.Limits.Access = ModelAccessAPIKey
			configuration.Server.APIKeys = []string{"a-client-key-of-reasonable-length"}
			testCase.apply(&configuration)
			err := configuration.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error naming %s", testCase.messageFragment)
			}
			if !strings.Contains(err.Error(), testCase.messageFragment) {
				t.Fatalf("Validate() = %v, want it to name %s", err, testCase.messageFragment)
			}
		})
	}
}

// Test flow:
//  1. For each case, start from the defaults, open one model to `api_key` callers with a valid client key, and apply the case's change.
//  2. The cases are a client key of exactly `minAPIKeyLength` characters, a drain timeout of exactly `MinDrainTimeoutSeconds`, and zero on each of the three knobs documented as "0 = no static bound".
//  3. Assert `Validate` accepts every case.
func TestValidateAcceptsTheMinimumKeyAndDrainAndZeroBounds(t *testing.T) {
	testCases := []struct {
		name  string
		apply func(*Config)
	}{
		{name: "client api key of the minimum length", apply: func(c *Config) { c.Server.APIKeys = []string{strings.Repeat("k", minAPIKeyLength)} }},
		{name: "drain timeout of the race deadlines", apply: func(c *Config) { c.Stream.DrainTimeoutSeconds = MinDrainTimeoutSeconds }},
		{name: "zero max concurrent requests", apply: func(c *Config) { c.Limits.Concurrency.MaxRequests = 0 }},
		{name: "zero max concurrent timeout votes", apply: func(c *Config) { c.Engine.MaxConcurrentTimeoutVotes = 0 }},
		{name: "zero max attempts per request", apply: func(c *Config) { c.Engine.MaxAttemptsPerRequest = 0 }},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			configuration := Defaults()
			configuration.Limits.Access = ModelAccessAPIKey
			configuration.Server.APIKeys = []string{"a-client-key-of-reasonable-length"}
			testCase.apply(&configuration)
			if err := configuration.Validate(); err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

// Test flow:
//  1. Build a config whose `models.<id>.access` is `api_key` and no active client key, and one whose network `access` is `api_key`.
//  2. Assert `CheckClientKeys` returns `ErrAPIKeyModelWithoutKey` for both.
//  3. Assert the same per-model config built with an active key, one whose only model is `open`, one whose `api_key` block names a model the rotation list no longer lists, and the defaults all pass the check.
func TestCheckClientKeysRefusesAnAPIKeyModelWithNoActiveKey(t *testing.T) {
	keyedModelSettings := map[string]ModelSettings{"model-a": {Access: stringPointer(ModelAccessAPIKey)}}
	stranded, err := Build(env.Values{}, Overrides{Models: keyedModelSettings})
	if err != nil {
		t.Fatalf("Build(): %v", err)
	}
	if err := stranded.CheckClientKeys(); !errors.Is(err, ErrAPIKeyModelWithoutKey) {
		t.Fatalf("CheckClientKeys() with a per-model api_key and no key = %v, want ErrAPIKeyModelWithoutKey", err)
	}

	networkWide := Defaults()
	networkWide.Limits.Access = ModelAccessAPIKey
	if err := networkWide.CheckClientKeys(); !errors.Is(err, ErrAPIKeyModelWithoutKey) {
		t.Fatalf("CheckClientKeys() with a network api_key and no key = %v, want ErrAPIKeyModelWithoutKey", err)
	}

	openOnly, err := Build(env.Values{}, Overrides{Models: map[string]ModelSettings{"model-a": {Access: stringPointer(ModelAccessOpen)}}})
	if err != nil {
		t.Fatalf("Build(): %v", err)
	}
	if err := openOnly.CheckClientKeys(); err != nil {
		t.Fatalf("CheckClientKeys() with only open models = %v, want nil", err)
	}

	served, err := Build(env.Values{}, Overrides{Models: keyedModelSettings}, "a-client-key-of-reasonable-length")
	if err != nil {
		t.Fatalf("Build(): %v", err)
	}
	if err := served.CheckClientKeys(); err != nil {
		t.Fatalf("CheckClientKeys() with a key = %v, want nil", err)
	}

	rotation := `[{"model_id":"model-a"}]`
	staleOverrides := Overrides{Models: map[string]ModelSettings{"model-gone": {Access: stringPointer(ModelAccessAPIKey)}}}
	staleOverrides.RotationModelsJSON = &rotation
	withStale, err := Build(env.Values{}, staleOverrides)
	if err != nil {
		t.Fatalf("Build(): %v", err)
	}
	if err := withStale.CheckClientKeys(); err != nil {
		t.Fatalf("CheckClientKeys() with only a stale api_key block = %v, want nil", err)
	}

	defaults := Defaults()
	if err := defaults.CheckClientKeys(); err != nil {
		t.Fatalf("CheckClientKeys() with no api_key model = %v, want nil", err)
	}
}
