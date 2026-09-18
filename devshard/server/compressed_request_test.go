package server

import (
	"bytes"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/transport"
)

const (
	compressedRequestEscrowID = "60453"
	gzipEncodingName          = "gzip"
)

// verifyingBinder runs the real auth step and reports what it recovered.
type verifyingBinder struct {
	verifier signing.Verifier
	escrowID string
	sender   *string
	bodySize *int
}

func (b verifyingBinder) BindOwnerChat(c echo.Context) (*transport.Server, error) {
	sender, body, err := transport.VerifyPOSTAuth(c, b.verifier, b.escrowID, transport.DefaultMaxBodySize)
	if err != nil {
		return nil, err
	}
	*b.sender = sender
	*b.bodySize = len(body)
	return nil, ErrInitializing
}

// newVerifyingRoutes mounts the production routes behind real auth.
func newVerifyingRoutes() (*echo.Echo, *string, *int) {
	var sender string
	var bodySize int
	e := echo.New()
	RegisterLazySessionRoutes(
		e.Group(""),
		payloadsOnlyResolver{resolves: compressedRequestEscrowID},
		verifyingBinder{
			verifier: signing.NewSecp256k1Verifier(),
			escrowID: compressedRequestEscrowID,
			sender:   &sender,
			bodySize: &bodySize,
		},
		nil,
	)
	return e, &sender, &bodySize
}

// signedRequest signs plainBody and sends wireBody.
func signedRequest(t *testing.T, signer signing.Signer, plainBody, wireBody []byte, contentEncoding string) *http.Request {
	t.Helper()
	timestamp := time.Now().Unix()
	signature, err := transport.SignRequest(signer, compressedRequestEscrowID, plainBody, timestamp)
	require.NoError(t, err)

	request := httptest.NewRequest(http.MethodPost,
		"/sessions/"+compressedRequestEscrowID+"/chat/completions", bytes.NewReader(wireBody))
	request.Header.Set(transport.HeaderSignature, hex.EncodeToString(signature))
	request.Header.Set(transport.HeaderTimestamp, strconv.FormatInt(timestamp, 10))
	if contentEncoding != "" {
		request.Header.Set("Content-Encoding", contentEncoding)
	}
	return request
}

// Compress after signing, decompress before auth: both halves must agree.
func TestCompressedRequestVerifiesThroughTheRealRoutes(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	e, sender, bodySize := newVerifyingRoutes()

	body := []byte(`{"diffs":[],"nonce":7,"payload":{"prompt":"` + strings.Repeat("A", 8192) + `"}}`)
	compressed := testutil.MustGzip(t, body)
	require.Less(t, len(compressed), len(body)/4)

	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, signedRequest(t, signer, body, compressed, gzipEncodingName))

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code, "auth passed; the session is simply not bound")
	require.Equal(t, signer.Address(), *sender, "the host must recover the sender that signed the plaintext")
	require.Equal(t, len(body), *bodySize, "auth must see the whole plaintext, not the compressed bytes")
}

func TestUncompressedRequestStillVerifies(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	e, sender, bodySize := newVerifyingRoutes()

	body := []byte(`{"diffs":[],"nonce":7}`)
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, signedRequest(t, signer, body, body, ""))

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Equal(t, signer.Address(), *sender,
		"compression is opt-in per request: a sender that does not compress keeps working")
	require.Equal(t, len(body), *bodySize)
}

// The cap must bound what the body decodes to, not what arrives.
func TestCompressedRequestCapAppliesToTheDecompressedBytes(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	e, _, _ := newVerifyingRoutes()

	bomb := make([]byte, transport.DefaultMaxBodySize+1)
	compressed := testutil.MustGzip(t, bomb)
	require.Less(t, int64(len(compressed)), transport.DefaultMaxBodySize/10,
		"the bomb must be small on the wire to be worth testing")

	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, signedRequest(t, signer, bomb, compressed, gzipEncodingName))

	require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
}

// Signing the encoding instead of the body recovers somebody else.
func TestSigningTheCompressedBytesIsRejected(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	e, sender, _ := newVerifyingRoutes()

	compressed := testutil.MustGzip(t, []byte(`{"diffs":[],"nonce":7}`))
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, signedRequest(t, signer, compressed, compressed, gzipEncodingName))

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code, "auth ran; the session is simply not bound")
	require.NotEmpty(t, *sender, "auth must have recovered somebody")
	require.NotEqual(t, signer.Address(), *sender,
		"recovery does not fail on the wrong bytes, it yields somebody else — "+
			"which real admission then rejects as sender-not-in-group")
}

// Runs before any caller is named, so it must never answer 5xx.
func TestMalformedGzipRequestIsTheSendersFault(t *testing.T) {
	e, _, _ := newVerifyingRoutes()

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

// No header to be wrong about, so it passes through to auth.
func TestEmptyBodyClaimingGzipIsNotAGzipFault(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	e, sender, bodySize := newVerifyingRoutes()

	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, signedRequest(t, signer, nil, nil, gzipEncodingName))

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code, "auth ran on the empty body")
	require.Equal(t, signer.Address(), *sender)
	require.Zero(t, *bodySize, "the body passed through as the empty body it is")
}

// A good header with a cut stream fails at the read, not the header check.
func TestTruncatedGzipBodyFailsWhereItIsRead(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	e, _, _ := newVerifyingRoutes()

	body := []byte(`{"diffs":[],"nonce":7,"payload":{"prompt":"` + strings.Repeat("A", 4096) + `"}}`)
	compressed := testutil.MustGzip(t, body)
	truncated := compressed[:len(compressed)/2]

	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, signedRequest(t, signer, body, truncated, gzipEncodingName))

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "read body",
		"a stream that dies while being read fails where it is read, not at the header check")
}

// Content codings are case-insensitive.
func TestCompressedRequestAcceptsAnUppercaseEncoding(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	e, sender, _ := newVerifyingRoutes()

	body := []byte(`{"diffs":[],"nonce":7}`)
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, signedRequest(t, signer, body, testutil.MustGzip(t, body), "GZIP"))

	require.Equal(t, signer.Address(), *sender)
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

// The decompression cleanup runs between the two.
func TestConsecutiveCompressedRequestsBothVerify(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	e, sender, bodySize := newVerifyingRoutes()

	for attempt := 0; attempt < 2; attempt++ {
		body := []byte(`{"diffs":[],"nonce":` + strconv.Itoa(attempt) + `,"payload":{"prompt":"` +
			strings.Repeat("A", 4096) + `"}}`)
		recorder := httptest.NewRecorder()
		e.ServeHTTP(recorder, signedRequest(t, signer, body, testutil.MustGzip(t, body), gzipEncodingName))

		require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
		require.Equal(t, signer.Address(), *sender)
		require.Equal(t, len(body), *bodySize)
	}
}
