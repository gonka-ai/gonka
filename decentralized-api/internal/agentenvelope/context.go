package agentenvelope

import (
	"time"

	"github.com/labstack/echo/v4"
)

// echoContextKey is the Echo context key under which a verified Context is
// stored by the middleware and read by the handler.
const echoContextKey = "aps_agent_envelope_context"

// Context is the verified result the middleware attaches to the request.
//
// RequestBound is the forward-compatibility guard from architectural principle
// P15. The middleware verifies the envelope crypto and scope and sets
// RequestBound to false. The handler sets it to true only after it confirms
// the envelope principal matches the parsed requester address, or resolves
// that link through an authz grant. Attribution is emitted only when
// RequestBound is true.
type Context struct {
	Envelope          *EnvelopeV1
	EnvelopeSHA256    string
	RequestBodySHA256 string
	VerifiedAt        time.Time
	RequestBound      bool
}

// SetEchoContext stores a verified Context on the Echo request context.
func SetEchoContext(c echo.Context, apsCtx *Context) {
	c.Set(echoContextKey, apsCtx)
}

// FromEchoContext returns the verified Context attached to the Echo request
// context, or nil if the request carried no agent envelope.
func FromEchoContext(c echo.Context) *Context {
	v := c.Get(echoContextKey)
	if v == nil {
		return nil
	}
	apsCtx, ok := v.(*Context)
	if !ok {
		return nil
	}
	return apsCtx
}
