package env

import "testing"

// Test flow:
//  1. Table-driven: each case sets the log-level environment variable to unset, blank, a level name or a padded one.
//  2. For each case, call `LogLevel`.
//  3. Assert it returns the trimmed value, and empty when the variable is unset or blank.
func TestLogLevelReturnsTheTrimmedVariable(t *testing.T) {
	testCases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "unset reads as empty", raw: "", want: ""},
		{name: "blank reads as empty", raw: "   ", want: ""},
		{name: "debug is returned as written", raw: "debug", want: "debug"},
		{name: "padding is trimmed", raw: " Warn ", want: "Warn"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("DEVSHARD_LOG_LEVEL", testCase.raw)

			if got := LogLevel(); got != testCase.want {
				t.Fatalf("LogLevel() = %q, want %q", got, testCase.want)
			}
		})
	}
}
