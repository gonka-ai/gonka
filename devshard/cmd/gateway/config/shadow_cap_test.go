package config

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"devshard/cmd/gateway/env"
)

const shadowHost = "gonka1shadowhost"

// Test flow:
//  1. Build with a network `max_tokens_cap` one below `ProbeMinOutputTokens` and a network shadow list.
//  2. Assert the build is refused as `ErrInvalid` naming `participants.shadow`, the cap and the floor.
//  3. Build with the cap at the floor and assert it passes, and with the low cap and no shadow list and assert it passes.
func TestANetworkShadowListIsRefusedBelowTheProbeFloor(t *testing.T) {
	t.Parallel()
	shadow := []string{shadowHost}
	below := int64(ProbeMinOutputTokens - 1)
	overrides := Overrides{Participants: &ParticipantOverrides{Shadow: &shadow}}
	overrides.MaxTokensCap = &below
	_, err := Build(env.Values{}, overrides)
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "participants.shadow") || !strings.Contains(err.Error(), strconv.Itoa(ProbeMinOutputTokens-1)) || !strings.Contains(err.Error(), strconv.Itoa(ProbeMinOutputTokens)) {
		t.Fatalf("Build() with a shadow list under the floor = %v, want ErrInvalid naming participants.shadow, the cap and the floor", err)
	}

	atFloor := int64(ProbeMinOutputTokens)
	overrides.MaxTokensCap = &atFloor
	if _, err := Build(env.Values{}, overrides); err != nil {
		t.Errorf("Build() with a shadow list and the cap at the floor = %v, want nil", err)
	}

	noShadow := Overrides{}
	noShadow.MaxTokensCap = &below
	if _, err := Build(env.Values{}, noShadow); err != nil {
		t.Errorf("Build() with no shadow list under the floor = %v, want nil", err)
	}
}

// Test flow:
//  1. Build with a model whose own `max_tokens_cap` is below the floor and whose own shadow list is set, beside a model at the floor.
//  2. Assert the build is refused naming `models[model-a].participants.shadow` and not model-b.
//  3. Build with the low-cap model inheriting a network shadow list and assert the refusal names `models[model-a]`.
//  4. Build with the low-cap model clearing its shadow list, under a network shadow list, and assert it passes.
func TestAModelShadowListIsRefusedBelowTheProbeFloor(t *testing.T) {
	t.Parallel()
	shadow := []string{shadowHost}
	none := []string{}
	below := int64(ProbeMinOutputTokens - 1)
	atFloor := int64(ProbeMinOutputTokens)

	own := map[string]ModelSettings{
		"model-a": {MaxTokensCap: &below, Participants: &ParticipantOverrides{Shadow: &shadow}},
		"model-b": {MaxTokensCap: &atFloor, Participants: &ParticipantOverrides{Shadow: &shadow}},
	}
	_, err := buildWithModels(t, nil, Overrides{Models: own})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "models[model-a].participants.shadow") || !strings.Contains(err.Error(), strconv.Itoa(ProbeMinOutputTokens)) || strings.Contains(err.Error(), "model-b") {
		t.Fatalf("Build() with a model shadow list under the floor = %v, want ErrInvalid naming only models[model-a].participants.shadow and the floor", err)
	}

	inherited := Overrides{Participants: &ParticipantOverrides{Shadow: &shadow}, Models: map[string]ModelSettings{"model-a": {MaxTokensCap: &below}}}
	_, err = buildWithModels(t, nil, inherited)
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "models[model-a].participants.shadow") {
		t.Fatalf("Build() with an inherited shadow list under the floor = %v, want ErrInvalid naming models[model-a].participants.shadow", err)
	}

	cleared := Overrides{Participants: &ParticipantOverrides{Shadow: &shadow}, Models: map[string]ModelSettings{"model-a": {MaxTokensCap: &below, Participants: &ParticipantOverrides{Shadow: &none}}}}
	if _, err := buildWithModels(t, nil, cleared); err != nil {
		t.Errorf("Build() with the low-cap model clearing its shadow list = %v, want nil", err)
	}
}

// Test flow:
//  1. Build with a rotation list naming model-a and a stored block for model-gone that sets a shadow list under a cap below the floor.
//  2. Assert the build passes, `Offers(model-gone)` is false and `CheckStoredModels` names model-gone, so the block is skipped rather than validated.
func TestAStoredBlockForAModelLeftOutOfRotationIsNotCheckedAgainstTheProbeFloor(t *testing.T) {
	t.Parallel()
	shadow := []string{shadowHost}
	below := int64(ProbeMinOutputTokens - 1)
	rotation := `[{"model_id":"model-a"}]`
	stale := Overrides{Models: map[string]ModelSettings{"model-gone": {MaxTokensCap: &below, Participants: &ParticipantOverrides{Shadow: &shadow}}}}
	stale.RotationModelsJSON = &rotation

	built, err := Build(env.Values{}, stale)
	if err != nil {
		t.Fatalf("Build() with a stale stored block under the floor = %v, want nil", err)
	}
	if stored := built.CheckStoredModels(); built.Offers("model-gone") || stored == nil || !strings.Contains(stored.Error(), "model-gone") {
		t.Errorf("Offers(model-gone) = %v, CheckStoredModels = %v, want the block skipped and named", built.Offers("model-gone"), stored)
	}
}

// Test flow:
//  1. Build with a network shadow list and a legacy `model_limits` entry whose cap is below the floor, beside one at the floor.
//  2. Assert the build is refused naming `models[model-a].participants.shadow` and the floor, and not model-b.
//  3. Build the same legacy entry with no shadow list and assert it passes.
func TestALegacyModelLimitUnderTheProbeFloorIsRefusedUnderAShadowList(t *testing.T) {
	t.Parallel()
	shadow := []string{shadowHost}
	below, atFloor := int64(ProbeMinOutputTokens-1), int64(ProbeMinOutputTokens)
	limits := map[string]LegacyModelLimits{
		"model-a": {DefaultMaxTokens: below, MaxTokensCap: below},
		"model-b": {DefaultMaxTokens: atFloor, MaxTokensCap: atFloor},
	}

	_, err := buildWithModels(t, nil, Overrides{Participants: &ParticipantOverrides{Shadow: &shadow}, ModelLimits: limits})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "models[model-a].participants.shadow") || !strings.Contains(err.Error(), strconv.Itoa(ProbeMinOutputTokens)) || strings.Contains(err.Error(), "model-b") {
		t.Fatalf("Build() with a legacy limit under the floor = %v, want ErrInvalid naming only models[model-a].participants.shadow and the floor", err)
	}

	if _, err := buildWithModels(t, nil, Overrides{ModelLimits: limits}); err != nil {
		t.Errorf("Build() with the legacy limits and no shadow list = %v, want nil", err)
	}
}
