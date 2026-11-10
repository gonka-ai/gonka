package config

import (
	"strings"
	"testing"
)

// Test flow:
//  1. For each case, start from the defaults, open one model to `api_key` callers with a valid client key, and apply the case's change.
//  2. The cases are a one-character client API key, a client key one character under `minAPIKeyLength` beside a valid one, a drain timeout of one second, and a drain timeout one second under `MinDrainTimeoutSeconds`.
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
		{name: "drain timeout of one second", apply: func(c *Config) { c.Stream.DrainTimeoutSeconds = 1 }, messageFragment: "drain_timeout_seconds"},
		{name: "drain timeout one under the race deadlines", apply: func(c *Config) { c.Stream.DrainTimeoutSeconds = MinDrainTimeoutSeconds - 1 }, messageFragment: "drain_timeout_seconds"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			configuration := Defaults()
			configuration.Limits.ModelAccess = map[string]string{"model-a": ModelAccessAPIKey}
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
//  2. The cases are a client key of exactly `minAPIKeyLength` characters, a long client key with a space inside it that no `Bearer` header can carry, a drain timeout of exactly `MinDrainTimeoutSeconds`, and zero on each of the three knobs documented as "0 = no static bound".
//  3. Assert `Validate` accepts every case.
func TestValidateAcceptsTheMinimumKeyAndDrainAndZeroBounds(t *testing.T) {
	testCases := []struct {
		name  string
		apply func(*Config)
	}{
		{name: "client api key of the minimum length", apply: func(c *Config) { c.Server.APIKeys = []string{strings.Repeat("k", minAPIKeyLength)} }},
		{name: "client api key with an inner space", apply: func(c *Config) { c.Server.APIKeys = []string{"a client key with spaces"} }},
		{name: "drain timeout of the race deadlines", apply: func(c *Config) { c.Stream.DrainTimeoutSeconds = MinDrainTimeoutSeconds }},
		{name: "zero max concurrent requests", apply: func(c *Config) { c.Limits.Concurrency.MaxRequests = 0 }},
		{name: "zero max concurrent timeout votes", apply: func(c *Config) { c.Engine.MaxConcurrentTimeoutVotes = 0 }},
		{name: "zero max attempts per request", apply: func(c *Config) { c.Engine.MaxAttemptsPerRequest = 0 }},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			configuration := Defaults()
			configuration.Limits.ModelAccess = map[string]string{"model-a": ModelAccessAPIKey}
			configuration.Server.APIKeys = []string{"a-client-key-of-reasonable-length"}
			testCase.apply(&configuration)
			if err := configuration.Validate(); err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}
