package filters

import (
	"encoding/json"
	"errors"
	"testing"
)

func floatPointer(value float64) *float64 { return &value }

func int64Pointer(value int64) *int64 { return &value }

func numberField(t *testing.T, document *Document, key string) float64 {
	t.Helper()
	raw, present := document.Get(key)
	number, isNumber := raw.(json.Number)
	if !present || !isNumber {
		t.Fatalf("%s = %v, want a number", key, raw)
	}
	value, err := number.Float64()
	if err != nil {
		t.Fatalf("%s = %v: %v", key, raw, err)
	}
	return value
}

// Test flow:
//  1. Bind Qwen to a temperature max of 1.2.
//  2. Assert a request asking 1.7 goes out at 1.2, and one asking 0.5 is left alone.
func TestTemperatureNarrowedToModelMax(t *testing.T) {
	knobs := FilterKnobs{Sampling: &SamplingLimits{TemperatureMax: floatPointer(1.2)}}

	high, errHigh := normalizeWithKnobs(t, knobs, `"temperature":1.7`)
	low, errLow := normalizeWithKnobs(t, knobs, `"temperature":0.5`)

	if errHigh != nil || errLow != nil {
		t.Fatalf("errors = %v, %v", errHigh, errLow)
	}
	if value := numberField(t, high, "temperature"); value != 1.2 {
		t.Errorf("temperature 1.7 became %v, want 1.2", value)
	}
	if value := numberField(t, low, "temperature"); value != 0.5 {
		t.Errorf("temperature 0.5 became %v, want 0.5", value)
	}
}

// Test flow:
//  1. Bind Qwen to top_k max 40 and top_p max 0.9.
//  2. Assert top_k -1 (no limit) and 100 both go out as 40, and top_p 0.95 as 0.9.
func TestTopKDisabledBecomesModelMax(t *testing.T) {
	knobs := FilterKnobs{Sampling: &SamplingLimits{TopKMax: int64Pointer(40), TopPMax: floatPointer(0.9)}}
	for _, requested := range []string{"-1", "100"} {
		document, err := normalizeWithKnobs(t, knobs, `"top_k":`+requested+`,"top_p":0.95`)
		if err != nil {
			t.Fatalf("top_k %s: error = %v", requested, err)
		}
		if value := numberField(t, document, "top_k"); value != 40 {
			t.Errorf("top_k %s became %v, want 40", requested, value)
		}
		if value := numberField(t, document, "top_p"); value != 0.9 {
			t.Errorf("top_p became %v, want 0.9", value)
		}
	}
}

// Test flow:
//  1. Compose limits that would widen a global bound or are not positive.
//  2. Assert each is refused as an invalid setting.
func TestNarrowingNeverWidens(t *testing.T) {
	cases := map[string]SamplingLimits{
		"temperature_above_global": {TemperatureMax: floatPointer(3)},
		"temperature_zero":         {TemperatureMax: floatPointer(0)},
		"top_p_above_one":          {TopPMax: floatPointer(1.5)},
		"min_p_negative":           {MinPMax: floatPointer(-0.1)},
		"repetition_above_global":  {RepetitionPenaltyMax: floatPointer(2.5)},
		"top_k_zero":               {TopKMax: int64Pointer(0)},
		"top_k_above_global":       {TopKMax: int64Pointer(topKMax + 1)},
	}
	for name, limits := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ComposeProfile(presetsTestQwenModel, ModelSetting{Filters: FilterKnobs{Sampling: &limits}}); !errors.Is(err, ErrInvalidModelSetting) {
				t.Errorf("ComposeProfile() error = %v, want ErrInvalidModelSetting", err)
			}
		})
	}
}

// Test flow:
//  1. Normalize a request with every sampled parameter under a profile without limits.
//  2. Assert each keeps the value the global rules give it.
func TestNoSamplingLimitsLeavesGlobalBehaviour(t *testing.T) {
	document, err := normalizeWithKnobs(t, FilterKnobs{}, `"temperature":1.9,"top_p":1,"top_k":-1,"min_p":0.7,"repetition_penalty":1.8`)
	if err != nil {
		t.Fatalf("NormalizeRequest() error = %v", err)
	}
	for field, want := range map[string]float64{"temperature": 1.9, "top_p": 1, "min_p": 0.7, "repetition_penalty": 1.8, "top_k": -1} {
		if value := numberField(t, document, field); value != want {
			t.Errorf("%s = %v, want %v", field, value, want)
		}
	}
}
