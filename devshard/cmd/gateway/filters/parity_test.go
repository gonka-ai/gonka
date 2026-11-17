package filters

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type parityCase struct {
	RoutedModel      string          `json:"routed_model"`
	Admin            bool            `json:"admin"`
	DefaultMaxTokens uint64          `json:"default_max_tokens"`
	MaxTokensCap     uint64          `json:"max_tokens_cap"`
	Body             json.RawMessage `json:"body"`
}

type parityGolden struct {
	Status     string `json:"status"`
	BodyBase64 string `json:"body_base64"`
	HTTPStatus int    `json:"http_status"`
	Message    string `json:"message"`
}

// Test flow:
//  1. Read every case file from testdata/corpus and its matching golden from testdata/goldens.
//  2. Run NormalizeRequest on each case's body with its recorded options.
//  3. For an "ok" golden, assert the result is accepted and its body matches the golden's body once forced fields are stripped from both sides.
//  4. For a "rejected" golden, assert the result is rejected with the golden's HTTP status and exact error message.
func TestGoldenParity(t *testing.T) {
	corpusEntries, err := os.ReadDir(filepath.Join("testdata", "corpus"))
	if err != nil {
		t.Fatalf("reading corpus: %v", err)
	}
	if len(corpusEntries) == 0 {
		t.Fatal("corpus is empty")
	}
	for _, entry := range corpusEntries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		t.Run(strings.TrimSuffix(entry.Name(), ".json"), func(t *testing.T) {
			rawCase, err := os.ReadFile(filepath.Join("testdata", "corpus", entry.Name()))
			if err != nil {
				t.Fatalf("reading case: %v", err)
			}
			var testCase parityCase
			if err := json.Unmarshal(rawCase, &testCase); err != nil {
				t.Fatalf("parsing case: %v", err)
			}
			rawGolden, err := os.ReadFile(filepath.Join("testdata", "goldens", entry.Name()))
			if err != nil {
				t.Fatalf("reading golden (a new corpus case needs its golden written from this pipeline's output): %v", err)
			}
			var golden parityGolden
			if err := json.Unmarshal(rawGolden, &golden); err != nil {
				t.Fatalf("parsing golden: %v", err)
			}

			result, err := NormalizeRequest(testCase.Body, Options{
				Admin:            testCase.Admin,
				DefaultMaxTokens: testCase.DefaultMaxTokens,
				MaxTokensCap:     testCase.MaxTokensCap,
				RoutedModel:      testCase.RoutedModel,
			})

			switch golden.Status {
			case "ok":
				if err != nil {
					t.Fatalf("golden accepted, result rejected: %v", err)
				}
				wantBody, decodeErr := base64.StdEncoding.DecodeString(golden.BodyBase64)
				if decodeErr != nil {
					t.Fatalf("decoding golden body: %v", decodeErr)
				}
				if got, want := withoutForcedFields(t, result.Body), withoutForcedFields(t, wantBody); !bytes.Equal(got, want) {
					t.Fatalf("body mismatch\n golden: %s\n got: %s", want, got)
				}
			case "rejected":
				if err == nil {
					t.Fatalf("golden rejected (%d %q), result accepted: %s", golden.HTTPStatus, golden.Message, result.Body)
				}
				if got := ErrorStatus(err, 400); got != golden.HTTPStatus {
					t.Fatalf("status mismatch: golden %d, got %d (%v)", golden.HTTPStatus, got, err)
				}
				if err.Error() != golden.Message {
					t.Fatalf("message mismatch\n golden: %q\n got: %q", golden.Message, err.Error())
				}
			default:
				t.Fatalf("golden has unknown status %q", golden.Status)
			}
		})
	}
}

func withoutForcedFields(t *testing.T, body []byte) []byte {
	t.Helper()
	document, err := ParseDocument(body)
	if err != nil {
		t.Fatalf("parsing body %s: %v", body, err)
	}
	document.Delete("stream")
	document.Delete("stream_options")
	document.Delete("n")
	document.Delete("return_token_ids")
	if kwargs, present, isObject := document.ObjectField("chat_template_kwargs"); present && isObject {
		delete(kwargs, "thinking")
		if len(kwargs) == 0 {
			document.Delete("chat_template_kwargs")
		}
	}
	stripped, err := document.Marshal()
	if err != nil {
		t.Fatalf("marshalling body %s: %v", body, err)
	}
	return stripped
}
