package env

import "testing"

// Test flow:
//  1. Table-driven: each case sets the log-format environment variable to unset, "json", "text", a differently-cased spelling, or an unknown value.
//  2. For each case, call `LogFormat`.
//  3. Assert the result is JSON when unset or asked for, and text for anything else, for the deployed variable.
func TestTheLogFormatIsJSONUnlessSomethingElseIsAskedFor(t *testing.T) {
	testCases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "unset reads as json", raw: "", want: LogFormatJSON},
		{name: "json stays json", raw: "json", want: LogFormatJSON},
		{name: "json is not case sensitive and is trimmed", raw: " JSON ", want: LogFormatJSON},
		{name: "text keeps the text form", raw: "text", want: LogFormatText},
		{name: "any other value keeps the text form, as it always has", raw: "logfmt", want: LogFormatText},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("DEVSHARD_LOG_FORMAT", testCase.raw)

			if got := LogFormat(); got != testCase.want {
				t.Fatalf("LogFormat() = %q, want %q", got, testCase.want)
			}
		})
	}
}
