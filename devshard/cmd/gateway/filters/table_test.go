package filters

import (
	"encoding/json"
	"testing"
)

// Test flow:
//  1. Inspect the length of `parameterTable`.
//  2. Assert it is not empty.
func TestTableIsNotEmpty(t *testing.T) {
	if len(parameterTable) == 0 {
		t.Fatal("parameterTable must not be empty")
	}
}

// Test flow:
//  1. Walk `parameterTable`, tracking names already seen.
//  2. Assert no entry's Name repeats an earlier one.
func TestTableNamesAreUnique(t *testing.T) {
	seen := make(map[string]bool, len(parameterTable))
	for _, spec := range parameterTable {
		if seen[spec.Name] {
			t.Errorf("parameterTable has a duplicate entry for %q", spec.Name)
		}
		seen[spec.Name] = true
	}
}

// Test flow:
//  1. Look up the framework-critical parameter names (model, stream, max_tokens, max_completion_tokens, messages) in `knownParameterSet`.
//  2. Assert each name is present.
func TestTableKnownParametersContainsFrameworkFields(t *testing.T) {
	for _, name := range []string{"model", "stream", "max_tokens", "max_completion_tokens", "messages"} {
		if _, ok := knownParameterSet[name]; !ok {
			t.Errorf("knownParameterSet missing %q", name)
		}
	}
}

// Test flow:
//  1. Compare the size of `knownParameterSet` against `parameterTable`.
//  2. Assert the two have the same number of entries.
//  3. Assert every `parameterTable` entry's Name is present in `knownParameterSet`.
func TestTableKnownParametersDerivedFromTable(t *testing.T) {
	if len(knownParameterSet) != len(parameterTable) {
		t.Fatalf("knownParameterSet has %d entries, want %d (one per parameterTable entry)", len(knownParameterSet), len(parameterTable))
	}
	for _, spec := range parameterTable {
		if _, ok := knownParameterSet[spec.Name]; !ok {
			t.Errorf("knownParameterSet missing table entry %q", spec.Name)
		}
	}
}

// Test flow:
//  1. Normalize request bodies that vary the "n" field between present (5) and absent.
//  2. Parse the normalized body back into a document.
//  3. Assert "n" is rewritten to json.Number("1") when present, and stays absent otherwise.
func TestTableForcesASingleChoice(t *testing.T) {
	for _, testCase := range []struct {
		name string
		body string
		want any
	}{
		{"present is rewritten", `{"messages":[{"role":"user","content":"hi"}],"n":5}`, json.Number("1")},
		{"absent stays absent", `{"messages":[{"role":"user","content":"hi"}]}`, nil},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := NormalizeRequest([]byte(testCase.body), Options{DefaultMaxTokens: 3072, MaxTokensCap: 3072})
			if err != nil {
				t.Fatalf("NormalizeRequest() = %v, want acceptance", err)
			}
			document, err := ParseDocument(result.Body)
			if err != nil {
				t.Fatalf("ParseDocument(%s) = %v", result.Body, err)
			}
			got, _ := document.Get("n")
			if got != testCase.want {
				t.Errorf("n = %#v, want %#v (body %s)", got, testCase.want, result.Body)
			}
		})
	}
}

// Test flow:
//  1. Normalize request bodies with return_token_ids set true, set false, and absent.
//  2. Parse the normalized body back into a document.
//  3. Assert return_token_ids is absent in every case: the executing host sets it itself.
func TestTableLeavesTokenIdsToTheHost(t *testing.T) {
	for _, testCase := range []struct {
		name string
		body string
	}{
		{"asked true", `{"messages":[{"role":"user","content":"hi"}],"return_token_ids":true}`},
		{"asked false", `{"messages":[{"role":"user","content":"hi"}],"return_token_ids":false}`},
		{"absent", `{"messages":[{"role":"user","content":"hi"}]}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := NormalizeRequest([]byte(testCase.body), Options{DefaultMaxTokens: 3072, MaxTokensCap: 3072})
			if err != nil {
				t.Fatalf("NormalizeRequest() = %v, want acceptance", err)
			}
			document, err := ParseDocument(result.Body)
			if err != nil {
				t.Fatalf("ParseDocument(%s) = %v", result.Body, err)
			}
			if document.Has("return_token_ids") {
				t.Errorf("return_token_ids reached the host: %s", result.Body)
			}
		})
	}
}
