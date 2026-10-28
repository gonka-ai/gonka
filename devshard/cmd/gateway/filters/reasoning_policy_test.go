package filters

import (
	"encoding/json"
	"errors"
	"testing"

	"devshard/cmd/gateway/internal/strictjson"
)

func normalizeWithKnobs(t *testing.T, knobs FilterKnobs, extraFields string) (*Document, error) {
	t.Helper()
	profile := composeForTest(t, presetsTestQwenModel, ModelSetting{Filters: knobs})
	body := `{"model":"` + presetsTestQwenModel + `","messages":[{"role":"user","content":"x"}],` + extraFields + `}`
	result, err := NormalizeRequest([]byte(body), Options{Profiles: NewProfileSet(map[string]*Profile{presetsTestQwenModel: profile})})
	if err != nil {
		return nil, err
	}
	return ParseDocument(result.Body)
}

// Test flow:
//  1. Bind Qwen to a profile that strips reasoning_effort.
//  2. Assert a request's reasoning_effort never reaches the host.
func TestReasoningEffortStripMode(t *testing.T) {
	document, err := normalizeWithKnobs(t, FilterKnobs{ReasoningEffort: &ReasoningEffortPolicy{Mode: ReasoningEffortStrip}}, `"reasoning_effort":"high"`)
	if err != nil {
		t.Fatalf("NormalizeRequest() error = %v", err)
	}
	if document.Has("reasoning_effort") {
		t.Error("reasoning_effort reached the host under strip mode")
	}
}

// Test flow:
//  1. Bind Qwen to a profile that allows only low and high.
//  2. Assert medium is refused and high is forwarded.
func TestReasoningEffortAllowedList(t *testing.T) {
	knobs := FilterKnobs{ReasoningEffort: &ReasoningEffortPolicy{Allowed: []string{"low", "high"}}}

	_, refused := normalizeWithKnobs(t, knobs, `"reasoning_effort":"medium"`)
	accepted, err := normalizeWithKnobs(t, knobs, `"reasoning_effort":"high"`)

	if ErrorStatus(refused, 0) != 400 {
		t.Errorf("medium: error = %v, want a 400 rejection", refused)
	}
	if err != nil || !accepted.Has("reasoning_effort") {
		t.Errorf("high: error = %v, want it forwarded", err)
	}
}

// Test flow:
//  1. Bind Qwen to a profile that allows only low and high.
//  2. Send the effort through the reasoning wrapper instead of the top-level field.
//  3. Assert it is refused the same way.
func TestReasoningWrapperEffortIsCheckedAgainstAllowed(t *testing.T) {
	_, err := normalizeWithKnobs(t, FilterKnobs{ReasoningEffort: &ReasoningEffortPolicy{Allowed: []string{"low", "high"}}}, `"reasoning":{"effort":"medium"}`)
	if ErrorStatus(err, 0) != 400 {
		t.Errorf("error = %v, want a 400 rejection", err)
	}
}

// Test flow:
//  1. Bind Qwen to a profile that keeps reasoning_split and defaults it to true.
//  2. Assert a request without the field gets true, and one that sends false keeps false.
func TestDefaultReasoningSplitFillsAbsentField(t *testing.T) {
	knobs := FilterKnobs{KeepReasoningSplit: boolPointer(true), DefaultReasoningSplit: boolPointer(true)}

	filled, errFilled := normalizeWithKnobs(t, knobs, `"stream":false`)
	kept, errKept := normalizeWithKnobs(t, knobs, `"reasoning_split":false`)

	if errFilled != nil || errKept != nil {
		t.Fatalf("errors = %v, %v", errFilled, errKept)
	}
	if value, _ := filled.Get("reasoning_split"); value != true {
		t.Errorf("absent field became %v, want true", value)
	}
	if value, _ := kept.Get("reasoning_split"); value != false {
		t.Errorf("client false became %v, want false kept", value)
	}
}

// Test flow:
//  1. Compose a reasoning_split default without keeping the field, an unknown allowed effort, and an unknown mode.
//  2. Assert each is refused as an invalid setting.
func TestComposeRefusesBadReasoningPolicies(t *testing.T) {
	cases := map[string]FilterKnobs{
		"default_split_without_keep": {DefaultReasoningSplit: boolPointer(true)},
		"unknown_allowed_effort":     {ReasoningEffort: &ReasoningEffortPolicy{Allowed: []string{"turbo"}}},
		"unknown_mode":               {ReasoningEffort: &ReasoningEffortPolicy{Mode: "drop"}},
	}
	for name, knobs := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ComposeProfile(presetsTestQwenModel, ModelSetting{Filters: knobs}); !errors.Is(err, ErrInvalidModelSetting) {
				t.Errorf("ComposeProfile() error = %v, want ErrInvalidModelSetting", err)
			}
		})
	}
}

// Test flow:
//  1. Write knobs whose reasoning_effort allows an explicitly empty list, the way the store does, and read them back.
//  2. Compose the read-back knobs over a base that allows only low.
//  3. Assert the empty list survived and widened the base to every value, as it did before the round trip.
func TestEmptyAllowedEffortListSurvivesTheStore(t *testing.T) {
	knobs := FilterKnobs{ReasoningEffort: &ReasoningEffortPolicy{Allowed: []string{}}}
	encoded, err := json.Marshal(knobs)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var readBack FilterKnobs
	if err := strictjson.DecodeKnownFields(encoded, &readBack); err != nil {
		t.Fatalf("DecodeKnownFields(%s) error = %v", encoded, err)
	}
	base, err := composeOver(*defaultProfile, FilterKnobs{ReasoningEffort: &ReasoningEffortPolicy{Allowed: []string{"low"}}})
	if err != nil {
		t.Fatalf("composeOver(base) error = %v", err)
	}

	widened, err := composeOver(*base, readBack)

	if err != nil {
		t.Fatalf("composeOver() error = %v", err)
	}
	if len(widened.ReasoningEffort.Allowed) != 0 {
		t.Errorf("allowed = %v after the round trip %s, want the empty list that accepts every value", widened.ReasoningEffort.Allowed, encoded)
	}
}
