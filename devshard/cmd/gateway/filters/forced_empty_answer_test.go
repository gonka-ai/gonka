package filters

import (
	"encoding/json"
	"reflect"
	"testing"
)

const forcedEndOfSequenceFields = `"logit_bias":{"2":100},"stop_token_ids":[1,2,3],"stop":["a","e","i","o","u"]`

// Test flow:
//  1. For each built-in model under its own preset, normalize a request whose logit_bias forces token 2 at the maximum bias of 100 and whose stop_token_ids and stop strings cover likely first tokens.
//  2. Assert the request is accepted and logit_bias, stop_token_ids and stop reach the host exactly as the client sent them.
func TestARequestThatForcesAnEmptyAnswerIsForwardedAsSent(t *testing.T) {
	for _, model := range []string{presetsTestQwenModel, kimiModelID, minimaxModelID, glm53FlashModelID} {
		t.Run(model, func(t *testing.T) {
			profile := BuiltInProfileFor(model)
			body := `{"model":"` + model + `","messages":[{"role":"user","content":"x"}],` + forcedEndOfSequenceFields + `}`
			result, err := NormalizeRequest([]byte(body), Options{
				Profiles:            NewProfileSet(map[string]*Profile{model: profile}),
				StopTokenVocabulary: fixedVocabulary(testVocabularySize),
			})
			if err != nil {
				t.Fatalf("NormalizeRequest() error = %v, want the request accepted", err)
			}
			var sent, forwarded map[string]any
			if err := json.Unmarshal([]byte(`{`+forcedEndOfSequenceFields+`}`), &sent); err != nil {
				t.Fatalf("unmarshal sent fields: %v", err)
			}
			if err := json.Unmarshal(result.Body, &forwarded); err != nil {
				t.Fatalf("unmarshal normalized body: %v", err)
			}
			for field, value := range sent {
				if !reflect.DeepEqual(forwarded[field], value) {
					t.Errorf("%s forwarded = %v, want %v as sent", field, forwarded[field], value)
				}
			}
		})
	}
}
