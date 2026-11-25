package filters

import (
	"errors"
	"net/http"
	"testing"
)

// Test flow:
//  1. Build an error case from the table: a Reject, a WrapReject around an oversized-body error, a WrapReject around a plain error, a plain non-RejectError, or nil.
//  2. Call ErrorStatus with the case's error and fallback status.
//  3. Assert the returned status matches the case's expectation.
func TestErrorsStatus(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		fallback int
		want     int
	}{
		{"Reject defaults to 400", Reject("boom"), 999, http.StatusBadRequest},
		{"an oversized body outranks the 400 of the RejectError wrapping it", WrapReject(&http.MaxBytesError{Limit: 10}), 999, http.StatusRequestEntityTooLarge},
		{"WrapReject defaults to 400", WrapReject(errors.New("inner")), 999, http.StatusBadRequest},
		{"non-RejectError falls back", errors.New("plain"), 418, 418},
		{"nil error falls back", nil, 500, 500},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ErrorStatus(testCase.err, testCase.fallback); got != testCase.want {
				t.Errorf("ErrorStatus() = %d, want %d", got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Build a Reject error with a formatted message.
//  2. Assert the error's message matches the formatted text.
func TestErrorsRejectFormatsMessage(t *testing.T) {
	err := Reject("field %q must be %d", "n", 5)
	want := `field "n" must be 5`
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

// Test flow:
//  1. Build a Reject error with no wrapped cause.
//  2. Assert errors.As resolves it to a *RejectError.
//  3. Assert Unwrap returns nil.
func TestErrorsUnwrapReturnsNilWithoutWrapped(t *testing.T) {
	err := Reject("boom")
	var rejectErr *RejectError
	if !errors.As(err, &rejectErr) {
		t.Fatalf("Reject() did not produce a *RejectError")
	}
	if unwrapped := rejectErr.Unwrap(); unwrapped != nil {
		t.Errorf("Unwrap() = %v, want nil", unwrapped)
	}
}

// Test flow:
//  1. Wrap a sentinel error with WrapReject.
//  2. Assert errors.Is still matches the sentinel through the wrapped error.
//  3. Assert the wrapped error's message equals the sentinel's message.
//  4. Assert ErrorStatus reports 400 for the wrapped error.
func TestErrorsWrapRejectPreservesChain(t *testing.T) {
	sentinel := errors.New("sentinel failure")
	wrapped := WrapReject(sentinel)

	if !errors.Is(wrapped, sentinel) {
		t.Fatalf("errors.Is(wrapped, sentinel) = false, want true")
	}
	if wrapped.Error() != sentinel.Error() {
		t.Errorf("Error() = %q, want %q", wrapped.Error(), sentinel.Error())
	}
	if got := ErrorStatus(wrapped, 0); got != http.StatusBadRequest {
		t.Errorf("ErrorStatus(wrapped) = %d, want %d", got, http.StatusBadRequest)
	}
}

// Test flow:
//  1. Call WrapReject with a nil error.
//  2. Assert the result is nil.
func TestErrorsWrapRejectNilReturnsNil(t *testing.T) {
	if err := WrapReject(nil); err != nil {
		t.Errorf("WrapReject(nil) = %v, want nil", err)
	}
}

// Test flow:
//  1. Call unsupportedParameterMessage with a rejected field name.
//  2. Assert the returned text matches the pinned wording verbatim, since every whitelist_ golden depends on this exact string.
func TestErrorsUnsupportedParameterMessageExactText(t *testing.T) {
	got := unsupportedParameterMessage("weird_field")
	want := `Chat completions parameter "weird_field" is currently rejected by the Gonka network. Some non-standard parameters can crash the vLLM engine on Gonka Host MLNodes, so the network rejects parameters that are not explicitly supported (see: https://github.com/gonka-ai/gonka/blob/main/docs/chat-api/README.md). If you do not need this parameter, remove it from the request; if you need it, file a request at https://github.com/gonka-ai/gonka/issues`
	if got != want {
		t.Errorf("unsupportedParameterMessage() mismatch\n got:  %q\n want: %q", got, want)
	}
}
