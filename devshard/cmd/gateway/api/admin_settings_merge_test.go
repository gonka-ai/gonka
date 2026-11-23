package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/env"
	"devshard/cmd/gateway/internal/logcapture"
	"devshard/cmd/gateway/internal/logkey"
)

func storedAccessAndKillSwitch(live *harness) {
	disabled, open := true, config.ModelAccessOpen
	live.control.overrides = config.Overrides{Tunables: env.Tunables{Disabled: &disabled}, Models: map[string]config.ModelSettings{"qwen": {Access: &open}}}
}

// Test flow:
//  1. Store overrides that open a model and switch the gateway off.
//  2. POST a settings body naming only a request cap.
//  3. Assert the applied overrides still open the model and keep the kill switch on, beside the new cap: a partial write changes only what it names.
func TestAPostedSettingsPatchKeepsWhatItDoesNotName(t *testing.T) {
	live := newHarness(t)
	storedAccessAndKillSwitch(live)

	recorder := live.request(t, http.MethodPost, "/v1/admin/settings", `{"max_concurrent_requests":250}`, adminHeaders())

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d (%s), want 200", recorder.Code, recorder.Body.String())
	}
	applied := live.operations.appliedOverrides()
	if applied.Models["qwen"].Access == nil || *applied.Models["qwen"].Access != config.ModelAccessOpen || applied.Disabled == nil || !*applied.Disabled {
		t.Fatalf("applied models %v disabled %v, want both kept", applied.Models, applied.Disabled)
	}
	if applied.MaxConcurrentRequests == nil || *applied.MaxConcurrentRequests != 250 {
		t.Fatalf("applied request cap %v, want 250", applied.MaxConcurrentRequests)
	}
}

// Test flow:
//  1. Store overrides that open a model and switch the gateway off.
//  2. PUT a settings body naming only a request cap.
//  3. Assert the applied overrides are that body alone: PUT writes the whole document.
func TestAPutSettingsDocumentReplacesTheStoredOne(t *testing.T) {
	live := newHarness(t)
	storedAccessAndKillSwitch(live)

	recorder := live.request(t, http.MethodPut, "/v1/admin/settings", `{"max_concurrent_requests":250}`, adminHeaders())

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d (%s), want 200", recorder.Code, recorder.Body.String())
	}
	applied := live.operations.appliedOverrides()
	if applied.Models != nil || applied.Disabled != nil || *applied.MaxConcurrentRequests != 250 {
		t.Fatalf("applied models %v disabled %v, want only the request cap", applied.Models, applied.Disabled)
	}
}

// Test flow:
//  1. Run a gateway whose network allowlist names `gonka1netwrk`, with `qwen` carrying its own allowlist and `other` a block that sets no list.
//  2. GET /v1/admin/state.
//  3. Assert `participants.network` holds the network lists, `qwen` shows only its own allowlist (the network list replaced, not joined) and `other` shows the network lists.
func TestTheAdminStateShowsEachModelsEffectiveParticipantLists(t *testing.T) {
	live := newHarness(t, func(configuration *config.Config) {
		configuration.Scheduler.ParticipantAllowlist = []string{"gonka1netwrk"}
		own, tier := []string{"gonka1qwenown"}, config.ModelAccessOpen
		mustResolveModels(configuration, map[string]config.ModelSettings{
			"qwen":  {ParticipantAllowlist: &own},
			"other": {Access: &tier},
		})
	})

	recorder := live.request(t, http.MethodGet, "/v1/admin/state", "", adminHeaders())

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d (%s), want 200", recorder.Code, recorder.Body.String())
	}
	var state struct {
		Participants participantsInForce `json:"participants"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &state); err != nil {
		t.Fatalf("state is no JSON: %v", err)
	}
	want := participantsInForce{
		Network: participantLists{Allowlist: []string{"gonka1netwrk"}, Unthrottled: []string{}},
		Models: map[string]participantLists{
			"qwen":  {Allowlist: []string{"gonka1qwenown"}, Unthrottled: []string{}},
			"other": {Allowlist: []string{"gonka1netwrk"}, Unthrottled: []string{}},
		},
	}
	if !reflect.DeepEqual(state.Participants, want) {
		t.Fatalf("participants = %+v, want %+v", state.Participants, want)
	}
}

// Test flow:
//  1. Run a gateway whose `qwen` carries its own unthrottled list.
//  2. POST an empty settings patch.
//  3. Assert the "settings written" audit line carries that model's effective lists under `participants_by_model`.
func TestTheSettingsAuditLineCarriesTheModelsParticipantLists(t *testing.T) {
	entries := logcapture.Install(t)
	live := newHarness(t, func(configuration *config.Config) {
		own := []string{"gonka1fastone"}
		mustResolveModels(configuration, map[string]config.ModelSettings{"qwen": {UnthrottledParticipants: &own}})
	})

	live.request(t, http.MethodPost, "/v1/admin/settings", `{}`, adminHeaders())

	entry, found := entries.Find("admin: settings written")
	if !found {
		t.Fatalf("no settings audit line: %+v", entries.All())
	}
	byModel, _ := logcapture.Field(entry, logkey.ParticipantsByModel).(map[string]participantLists)
	if got := byModel["qwen"].Unthrottled; len(got) != 1 || got[0] != "gonka1fastone" {
		t.Errorf("%s = %v, want qwen unthrottled [gonka1fastone]", logkey.ParticipantsByModel, logcapture.Field(entry, logkey.ParticipantsByModel))
	}
}
