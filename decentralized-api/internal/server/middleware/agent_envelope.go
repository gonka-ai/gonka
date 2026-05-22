package middleware

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"decentralized-api/internal/agentenvelope"

	"github.com/labstack/echo/v4"
)

const (
	headerPassport = "X-Agent-Passport"
	headerAgentSig = "X-Agent-Sig"
)

// AgentEnvelopeMiddleware returns Echo middleware that verifies an optional
// signed agent request envelope on inference completion endpoints.
//
// When the X-Agent-Passport header is absent, the request passes through with
// behavior identical to a node that does not run this middleware. When the
// header is present, the middleware decodes and verifies the envelope and the
// per-request agent signature, then attaches a verified agentenvelope.Context
// to the Echo context for the handler. A verification failure ends the request
// with the mapped HTTP status.
//
// The verified context carries RequestBound false. The handler sets it true
// only after it confirms the envelope principal matches the request's
// requester address, directly or through an authz grant.
//
// enabled gates the whole middleware: when false it is a pure passthrough, so
// the middleware can be mounted unconditionally and toggled by config.
func AgentEnvelopeMiddleware(enabled bool, cfg agentenvelope.Config, maxBodyBytes int64) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if !enabled {
				return next(c)
			}
			req := c.Request()
			passportHdr := req.Header.Get(headerPassport)
			sigHdr := req.Header.Get(headerAgentSig)

			// Step 1: header presence. An absent passport is a passthrough.
			// One header without the other is a malformed request.
			if passportHdr == "" && sigHdr == "" {
				return next(c)
			}
			if passportHdr == "" || sigHdr == "" {
				return envelopeHTTPError(agentenvelope.ErrSigHeaderMismatch)
			}

			// Step 2: decode the passport header to envelope JSON. The
			// signature header is decoded only to reject a malformed header as
			// 400; the verifier re-decodes it from the original string.
			envelopeJSON, err := base64.StdEncoding.DecodeString(passportHdr)
			if err != nil {
				return envelopeHTTPError(agentenvelope.ErrHeaderMalformed)
			}
			if _, err := base64.StdEncoding.DecodeString(sigHdr); err != nil {
				return envelopeHTTPError(agentenvelope.ErrHeaderMalformed)
			}

			// Steps 16 and 17: bounded body read, then restore for the handler.
			body, err := readBoundedBody(c, maxBodyBytes)
			if err != nil {
				return err
			}

			model, merr := extractModel(body)
			if merr != nil {
				return mapEnvelopeError(merr)
			}
			apsCtx, verr := cfg.Verify(envelopeJSON, sigHdr, req.Method, req.URL.RequestURI(), body, model, time.Now().UTC())
			if verr != nil {
				return mapEnvelopeError(verr)
			}

			// Step 21: attach the verified context for the handler.
			agentenvelope.SetEchoContext(c, apsCtx)
			return next(c)
		}
	}
}

// readBoundedBody reads at most maxBytes from the request body, then restores
// the body so the downstream handler can read it again. A body over the limit
// returns 413; any other read error is treated as a malformed request.
func readBoundedBody(c echo.Context, maxBytes int64) ([]byte, error) {
	limited := http.MaxBytesReader(c.Response().Writer, c.Request().Body, maxBytes)
	body, err := io.ReadAll(limited)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, envelopeHTTPError(agentenvelope.ErrBodyTooLarge)
		}
		return nil, envelopeHTTPError(agentenvelope.ErrEnvelopeMalformed)
	}
	c.Request().Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

// extractModel pulls the model field from an OpenAI-style request body. The
// body is part of the signed request, so a body that does not parse, or that
// carries no model, is a malformed request rather than a scope failure: it
// returns ErrEnvelopeMalformed (400) instead of an empty model that the scope
// check would later read as a 403.
func extractModel(body []byte) (string, error) {
	var parsed struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", agentenvelope.ErrEnvelopeMalformed
	}
	if parsed.Model == "" {
		return "", agentenvelope.ErrEnvelopeMalformed
	}
	return parsed.Model, nil
}

// mapEnvelopeError converts a verifier error into an Echo HTTP error. Verify
// only ever returns *agentenvelope.EnvelopeError; anything else is treated as
// an internal error.
func mapEnvelopeError(err error) error {
	var ee *agentenvelope.EnvelopeError
	if errors.As(err, &ee) {
		return envelopeHTTPError(ee)
	}
	return echo.NewHTTPError(http.StatusInternalServerError, "agentenvelope: internal verification error")
}

func envelopeHTTPError(ee *agentenvelope.EnvelopeError) error {
	return echo.NewHTTPError(ee.HTTPStatus(), ee.Code())
}
