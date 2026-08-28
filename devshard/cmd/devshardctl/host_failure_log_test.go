package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/transport"
)

func loggedField(t *testing.T, fields []any, name string) any {
	t.Helper()
	for index := 0; index+1 < len(fields); index += 2 {
		if fields[index] == name {
			return fields[index+1]
		}
	}
	return nil
}

func TestHostFailureLogFields_SeparatesTheStatusFromTheBodyAHostReturned(t *testing.T) {
	inf := &inflight{err: &transport.UpstreamStatusError{Path: "/inference", StatusCode: 503, Body: "upstream busy"}}

	fields := hostFailureLogFields(inf, nil)

	require.Equal(t, "http_503", loggedField(t, fields, "failure_reason"))
	require.Equal(t, 503, loggedField(t, fields, "http_status"))
	require.Equal(t, "/inference", loggedField(t, fields, "host_path"))
	require.Equal(t, "upstream busy", loggedField(t, fields, "error_body_sample"))
	require.Equal(t, false, loggedField(t, fields, "error_body_sample_truncated"))
	require.Equal(t, "http /inference: status 503", loggedField(t, fields, "error"))
}

// Nothing bounds the body a host puts in its error, so the log line must bound it itself.
func TestHostFailureLogFields_BoundsAnOversizedBody(t *testing.T) {
	body := strings.Repeat("x", emptyStreamBodySampleLimit+1024)
	inf := &inflight{err: &transport.UpstreamStatusError{Path: "/inference", StatusCode: 500, Body: body}}

	fields := hostFailureLogFields(inf, nil)

	require.Len(t, loggedField(t, fields, "error_body_sample"), emptyStreamBodySampleLimit)
	require.Equal(t, true, loggedField(t, fields, "error_body_sample_truncated"))
	require.NotContains(t, loggedField(t, fields, "error"), "xxx")
}

func TestHostFailureLogFields_NamesTheWayTheStreamDied(t *testing.T) {
	dialFailure := errors.New("dial tcp 10.0.0.1:8080: connect: connection refused")

	cases := []struct {
		name       string
		err        error
		wantReason string
	}{
		{"host never answered", dialFailure, "transport_error"},
		{"stream ended without a terminator", transport.ErrSSEStreamTruncated, "sse_truncated"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fields := hostFailureLogFields(&inflight{err: testCase.err}, nil)

			require.Equal(t, testCase.wantReason, loggedField(t, fields, "failure_reason"))
			require.Equal(t, testCase.err, loggedField(t, fields, "error"))
			require.Nil(t, loggedField(t, fields, "http_status"))
		})
	}
}
