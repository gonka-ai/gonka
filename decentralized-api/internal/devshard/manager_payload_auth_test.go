package devshard

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"decentralized-api/utils"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"devshard/observability"
	"devshard/types"
)

// TestAuthenticatePayloadRequest_HeaderAndWindowBranches exercises the
// pre-bridge validation branches (8 reasons) of authenticatePayloadRequest.
// Bridge / pubkey / signature branches require richer mocks and are
// covered separately.
func TestAuthenticatePayloadRequest_HeaderAndWindowBranches(t *testing.T) {
	now := time.Now()
	tooOld := now.Add(-2 * time.Minute).UnixNano()
	tooNew := now.Add(2 * time.Minute).UnixNano()
	ok := now.UnixNano()

	type kv struct{ k, v string }
	hdr := func(items ...kv) []kv { return items }

	tests := []struct {
		name       string
		headers    []kv
		wantReason observability.Reason
		wantStatus int
	}{
		{
			name:       "missing validator header",
			headers:    nil,
			wantReason: observability.ReasonMissingValidatorHeader,
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "missing timestamp header",
			headers: hdr(
				kv{utils.XValidatorAddressHeader, "addr"},
			),
			wantReason: observability.ReasonMissingTimestampHeader,
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "missing epoch header",
			headers: hdr(
				kv{utils.XValidatorAddressHeader, "addr"},
				kv{utils.XTimestampHeader, "1"},
			),
			wantReason: observability.ReasonMissingEpochHeader,
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "missing signature header",
			headers: hdr(
				kv{utils.XValidatorAddressHeader, "addr"},
				kv{utils.XTimestampHeader, "1"},
				kv{utils.XEpochIdHeader, "0"},
			),
			wantReason: observability.ReasonMissingSignatureHeader,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "invalid timestamp",
			headers: hdr(
				kv{utils.XValidatorAddressHeader, "addr"},
				kv{utils.XTimestampHeader, "abc"},
				kv{utils.XEpochIdHeader, "0"},
				kv{utils.AuthorizationHeader, "sig"},
			),
			wantReason: observability.ReasonInvalidTimestamp,
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "invalid epoch",
			headers: hdr(
				kv{utils.XValidatorAddressHeader, "addr"},
				kv{utils.XTimestampHeader, "1"},
				kv{utils.XEpochIdHeader, "abc"},
				kv{utils.AuthorizationHeader, "sig"},
			),
			wantReason: observability.ReasonInvalidEpoch,
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "timestamp too old",
			headers: hdr(
				kv{utils.XValidatorAddressHeader, "addr"},
				kv{utils.XTimestampHeader, fmtInt(tooOld)},
				kv{utils.XEpochIdHeader, "0"},
				kv{utils.AuthorizationHeader, "sig"},
			),
			wantReason: observability.ReasonTimestampTooOld,
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "timestamp in future",
			headers: hdr(
				kv{utils.XValidatorAddressHeader, "addr"},
				kv{utils.XTimestampHeader, fmtInt(tooNew)},
				kv{utils.XEpochIdHeader, "0"},
				kv{utils.AuthorizationHeader, "sig"},
			),
			wantReason: observability.ReasonTimestampInFuture,
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "not group member (bridge stage)",
			headers: hdr(
				kv{utils.XValidatorAddressHeader, "addr-not-in-group"},
				kv{utils.XTimestampHeader, fmtInt(ok)},
				kv{utils.XEpochIdHeader, "0"},
				kv{utils.AuthorizationHeader, "sig"},
			),
			wantReason: observability.ReasonNotGroupMember,
			wantStatus: http.StatusUnauthorized,
		},
	}

	m := &HostManager{}
	e := echo.New()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/?inference_id=ABC", strings.NewReader(""))
			for _, h := range tc.headers {
				req.Header.Set(h.k, h.v)
			}
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			_, gotReason, err := m.authenticatePayloadRequest(c, []types.SlotAssignment{})
			require.Error(t, err)
			require.Equal(t, tc.wantReason, gotReason, "reason")

			httpErr, ok := err.(*echo.HTTPError)
			require.True(t, ok, "want *echo.HTTPError, got %T", err)
			require.Equal(t, tc.wantStatus, httpErr.Code, "status")
		})
	}
}

func fmtInt(n int64) string {
	return strconv.FormatInt(n, 10)
}
