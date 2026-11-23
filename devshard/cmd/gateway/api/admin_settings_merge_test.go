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
//  1. Store overrides naming a trust list and a shadow list.
//  2. POST a settings body naming only `participants.shadow`.
//  3. Assert the applied trust list is kept and the shadow list is the posted one.
//  4. POST `participants.trust` as null and assert it is cleared while the shadow list stays.
func TestAPostedParticipantsPatchMergesPerSubKey(t *testing.T) {
	live := newHarness(t)
	trust, shadow := []string{"gonka1trust"}, []string{"gonka1shadow"}
	live.control.overrides = config.Overrides{Participants: &config.ParticipantOverrides{Trust: &trust, Shadow: &shadow}}

	live.request(t, http.MethodPost, "/v1/admin/settings", `{"participants":{"shadow":["gonka1other"]}}`, adminHeaders())

	applied := live.operations.appliedOverrides().Participants
	if applied == nil || applied.Trust == nil || (*applied.Trust)[0] != "gonka1trust" || applied.Shadow == nil || (*applied.Shadow)[0] != "gonka1other" {
		t.Fatalf("applied participants = %+v, want trust kept and shadow replaced", applied)
	}

	live.control.overrides = live.operations.appliedOverrides()
	live.request(t, http.MethodPost, "/v1/admin/settings", `{"participants":{"trust":null}}`, adminHeaders())

	cleared := live.operations.appliedOverrides().Participants
	if cleared == nil || cleared.Trust != nil || cleared.Shadow == nil {
		t.Fatalf("cleared participants = %+v, want trust cleared and shadow kept", cleared)
	}
}

// Test flow:
//  1. Store overrides naming a trust list.
//  2. PUT a settings body naming only `participants.shadow`.
//  3. Assert the applied participants are that document alone: the stored trust list is dropped.
func TestAPutSettingsDocumentReplacesTheStoredParticipants(t *testing.T) {
	live := newHarness(t)
	trust := []string{"gonka1trust"}
	live.control.overrides = config.Overrides{Participants: &config.ParticipantOverrides{Trust: &trust}}

	live.request(t, http.MethodPut, "/v1/admin/settings", `{"participants":{"shadow":["gonka1shadow"]}}`, adminHeaders())

	applied := live.operations.appliedOverrides().Participants
	if applied == nil || applied.Trust != nil || applied.Shadow == nil {
		t.Fatalf("applied participants = %+v, want shadow alone", applied)
	}
}

// Test flow:
//  1. Start a gateway whose configuration in force names a trust, a shadow and an unthrottled list, and capture the log.
//  2. POST an empty settings patch.
//  3. Assert the "settings written" audit line carries all three lists as they stand.
func TestTheSettingsAuditLineCarriesTheThreeParticipantLists(t *testing.T) {
	entries := logcapture.Install(t)
	live := newHarness(t, func(configuration *config.Config) {
		configuration.Scheduler.Participants = config.Participants{Trust: []string{"gonka1trust"}, Shadow: []string{"gonka1shadow"}, Unthrottled: []string{"gonka1fast"}}
	})

	live.request(t, http.MethodPost, "/v1/admin/settings", `{}`, adminHeaders())

	entry, found := entries.Find("admin: settings written")
	if !found {
		t.Fatalf("no settings audit line: %+v", entries.All())
	}
	for key, want := range map[string]string{
		logkey.ParticipantsTrust: "gonka1trust", logkey.ParticipantsShadow: "gonka1shadow", logkey.ParticipantsUnthrottled: "gonka1fast",
	} {
		if got, _ := logcapture.Field(entry, key).([]string); len(got) != 1 || got[0] != want {
			t.Errorf("%s = %v, want [%s]", key, logcapture.Field(entry, key), want)
		}
	}
}

// Test flow:
//  1. Run a gateway whose network lists shadow `gonka1net`, with `qwen` carrying its own shadow list and `other` a block that sets no list.
//  2. GET /v1/admin/state.
//  3. Assert `participants.network` holds the network lists, `qwen` shows only its own shadow list (the network list replaced, not joined) and `other` shows the network lists.
func TestTheAdminStateShowsEachModelsEffectiveParticipantLists(t *testing.T) {
	live := newHarness(t, func(configuration *config.Config) {
		configuration.Scheduler.Participants = config.Participants{Shadow: []string{"gonka1net"}, Trust: []string{"gonka1trust"}}
		own, tier := []string{"gonka1qwen"}, config.ModelAccessOpen
		mustResolveModels(configuration, map[string]config.ModelSettings{
			"qwen":  {Participants: &config.ParticipantOverrides{Shadow: &own}},
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
		Network: participantLists{Trust: []string{"gonka1trust"}, Shadow: []string{"gonka1net"}, Unthrottled: []string{}},
		Models: map[string]participantLists{
			"qwen":  {Trust: []string{"gonka1trust"}, Shadow: []string{"gonka1qwen"}, Unthrottled: []string{}},
			"other": {Trust: []string{"gonka1trust"}, Shadow: []string{"gonka1net"}, Unthrottled: []string{}},
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
		own := []string{"gonka1fast"}
		mustResolveModels(configuration, map[string]config.ModelSettings{"qwen": {Participants: &config.ParticipantOverrides{Unthrottled: &own}}})
	})

	live.request(t, http.MethodPost, "/v1/admin/settings", `{}`, adminHeaders())

	entry, found := entries.Find("admin: settings written")
	if !found {
		t.Fatalf("no settings audit line: %+v", entries.All())
	}
	byModel, _ := logcapture.Field(entry, logkey.ParticipantsByModel).(map[string]participantLists)
	if got := byModel["qwen"].Unthrottled; len(got) != 1 || got[0] != "gonka1fast" {
		t.Errorf("%s = %v, want qwen unthrottled [gonka1fast]", logkey.ParticipantsByModel, logcapture.Field(entry, logkey.ParticipantsByModel))
	}
}
