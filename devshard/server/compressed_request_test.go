package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"devshard/transport"
)

const (
	compressedRequestEscrowID = "60453"
	gzipEncodingName          = "gzip"
)

// A gzip header that is not gzip is still the sender's fault, before the
// retired handler answers.
func TestMalformedGzipRequestIsTheSendersFault(t *testing.T) {
	e := echo.New()
	RegisterLazySessionRoutes(e.Group(""), payloadsOnlyResolver{resolves: compressedRequestEscrowID}, countingBinder{n: new(int)}, nil)

	for _, testCase := range []struct {
		name string
		wire string
	}{
		{"wrong magic", "this is not gzip"},
		{"header cut short", "\x1f\x8b\x08"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost,
				"/sessions/"+compressedRequestEscrowID+"/chat/completions", strings.NewReader(testCase.wire))
			request.Header.Set("Content-Encoding", gzipEncodingName)
			recorder := httptest.NewRecorder()
			e.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Contains(t, recorder.Body.String(), "malformed gzip",
				"the header check must name itself, not borrow the read-body error")
		})
	}
}

func TestRetiredChatDoesNotBind(t *testing.T) {
	var n int
	e := echo.New()
	RegisterLazySessionRoutes(e.Group(""), payloadsOnlyResolver{resolves: compressedRequestEscrowID}, countingBinder{n: &n}, nil)

	request := httptest.NewRequest(http.MethodPost, "/sessions/"+compressedRequestEscrowID+"/chat/completions", strings.NewReader(`{}`))
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusGone, recorder.Code)
	require.Equal(t, transport.DevshardErrorHTTPSessionRetired, recorder.Header().Get(transport.HeaderDevshardError))
	require.Contains(t, recorder.Body.String(), transport.HTTPSessionRetiredMessage)
	require.Zero(t, n)
}
