package api

import (
	"net/http"
	"testing"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/env"
	"devshard/cmd/gateway/internal/logcapture"
	"devshard/cmd/gateway/internal/logkey"
)

func storedAccessAndKillSwitch(live *harness) {
	disabled := true
	live.control.overrides = config.Overrides{Tunables: env.Tunables{Disabled: &disabled}, ModelAccess: map[string]string{"qwen": config.ModelAccessOpen}}
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
	if applied.ModelAccess["qwen"] != config.ModelAccessOpen || applied.Disabled == nil || !*applied.Disabled {
		t.Fatalf("applied access %v disabled %v, want both kept", applied.ModelAccess, applied.Disabled)
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
	if applied.ModelAccess != nil || applied.Disabled != nil || *applied.MaxConcurrentRequests != 250 {
		t.Fatalf("applied access %v disabled %v, want only the request cap", applied.ModelAccess, applied.Disabled)
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
