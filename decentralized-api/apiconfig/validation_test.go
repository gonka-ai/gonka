package apiconfig

import "testing"

// A BaseURL-mode node has no host/ports; it must validate (the old unconditional
// host/port checks would have rejected it).
func TestValidateInferenceNodeBasic_BaseURLNodeValid(t *testing.T) {
	node := InferenceNodeConfig{
		Id:            "n1",
		MaxConcurrent: 1,
		Models:        map[string]ModelConfig{"m": {}},
		BaseURL:       "https://svc.provider.com/path",
	}
	if errs := ValidateInferenceNodeBasic(node); len(errs) != 0 {
		t.Errorf("expected valid baseURL node, got errors: %v", errs)
	}
}

// Setting both host+ports and base_url is rejected (exactly one mode).
func TestValidateInferenceNodeBasic_BothModesRejected(t *testing.T) {
	node := InferenceNodeConfig{
		Id:            "n1",
		MaxConcurrent: 1,
		Models:        map[string]ModelConfig{"m": {}},
		Host:          "h", InferencePort: 5000, PoCPort: 8080,
		BaseURL: "https://svc",
	}
	if errs := ValidateInferenceNodeBasic(node); len(errs) == 0 {
		t.Error("expected rejection when both host+ports and base_url set")
	}
}

// Host-Port nodes still require id, models, max_concurrent (non-addressing checks
// stay in ValidateInferenceNodeBasic).
func TestValidateInferenceNodeBasic_HostPortStillRequiresModels(t *testing.T) {
	node := InferenceNodeConfig{Id: "n1", MaxConcurrent: 1, Host: "h", InferencePort: 5000, PoCPort: 8080}
	errs := ValidateInferenceNodeBasic(node)
	found := false
	for _, e := range errs {
		if e == "at least one model must be specified" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected models-required error, got %v", errs)
	}
}
