package completionapi

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// Three packages branch on this answer; one that disagrees with the processor waits for a body it never gets.
func TestIsEventStream(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		contentType string
		want        bool
	}{
		{name: "a stream", contentType: "text/event-stream", want: true},
		{name: "a stream naming its charset", contentType: "text/event-stream; charset=utf-8", want: true},
		{name: "json", contentType: "application/json"},
		{name: "no content type at all"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			response := &http.Response{Header: http.Header{}}
			if testCase.contentType != "" {
				response.Header.Set("Content-Type", testCase.contentType)
			}
			require.Equal(t, testCase.want, IsEventStream(response))
		})
	}
}
