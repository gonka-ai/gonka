package filters

import (
	"fmt"
	"reflect"
	"testing"

	"common/completionapi"
)

const logitBiasRequest = `{"model":"m","messages":[{"role":"user","content":"hi"}],"logit_bias":%s}`

func normalizedLogitBias(t *testing.T, biases string, vocabulary func(string) int) any {
	t.Helper()
	document, _, err := normalizeWith(t, fmt.Sprintf(logitBiasRequest, biases), "m", vocabulary)
	if err != nil {
		t.Fatalf("NormalizeRequest() = %v, want nil", err)
	}
	return document[logitBiasField]
}

// Test flow:
//  1. Normalize a request whose logit_bias holds a key inside the routed model's vocabulary and one past it, with the vocabulary known.
//  2. Normalize the same request with the vocabulary unknown, and one whose only key is past the vocabulary.
//  3. Assert the key past a known vocabulary is dropped, the fallback limit applies when it is unknown, and an emptied map is removed.
func TestLogitBiasKeysPastTheVocabularyAreDropped(t *testing.T) {
	known := normalizedLogitBias(t, `{"5":1,"1000":2}`, fixedVocabulary(testVocabularySize))
	unknown := normalizedLogitBias(t, `{"5":1,"1000":2,"10000000":3}`, fixedVocabulary(VocabularySizeUnknown))
	emptied := normalizedLogitBias(t, `{"1000":2}`, fixedVocabulary(testVocabularySize))

	if want := map[string]any{"5": float64(1)}; !reflect.DeepEqual(known, want) {
		t.Errorf("logit_bias with a known vocabulary = %v, want %v", known, want)
	}
	if completionapi.UnknownVocabularyTokenIDLimit <= 1000 {
		t.Fatalf("precondition: UnknownVocabularyTokenIDLimit = %d, want above 1000", completionapi.UnknownVocabularyTokenIDLimit)
	}
	if want := map[string]any{"5": float64(1), "1000": float64(2)}; !reflect.DeepEqual(unknown, want) {
		t.Errorf("logit_bias with an unknown vocabulary = %v, want %v", unknown, want)
	}
	if emptied != nil {
		t.Errorf("logit_bias emptied by the drop = %v, want it removed", emptied)
	}
}
