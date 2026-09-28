package accounting

import "testing"

func TestADetailReasonOutsideTheVocabularyIsCollapsed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		reason string
		want   string
	}{
		{name: "a reason the vocabulary knows", reason: "empty_stream", want: "empty_stream"},
		{name: "the reason a client leaving produces", reason: DeliveryClientGone, want: DeliveryClientGone},
		{name: "a string the host invented", reason: "something_the_host_made_up", want: "unknown"},
		{name: "a near miss", reason: "empty_streams", want: "unknown"},
		{name: "surrounding space is not a new reason", reason: "  empty_stream  ", want: "empty_stream"},
		{name: "an empty reason", reason: ""},
		{name: "the explicit none", reason: "none"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := normalizeDetailReason(testCase.reason); got != testCase.want {
				t.Fatalf("normalizeDetailReason(%q) = %q, want %q", testCase.reason, got, testCase.want)
			}
		})
	}
}
