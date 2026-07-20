package observability

import (
	"github.com/labstack/echo/v4"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "edge-api.server"

var untracedRoutes = map[string]struct{}{
	"/healthz": {},
}

// EchoMiddleware extracts W3C trace context and opens a server-side span per request.
func EchoMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			req := c.Request()
			route := c.Path()
			if route == "" {
				route = req.URL.Path
			}
			if _, skip := untracedRoutes[route]; skip {
				return next(c)
			}

			ctx := otel.GetTextMapPropagator().Extract(req.Context(), propagation.HeaderCarrier(req.Header))
			ctx, span := otel.Tracer(tracerName).Start(
				ctx,
				"edge-api.request",
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					attribute.String("http.method", req.Method),
					attribute.String("http.route", route),
				),
			)
			c.SetRequest(req.WithContext(ctx))

			err := next(c)

			status := c.Response().Status
			if err != nil {
				if httpErr, ok := err.(*echo.HTTPError); ok {
					status = httpErr.Code
				}
			}
			span.SetAttributes(attribute.Int("http.status_code", status))
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
			} else {
				span.SetStatus(codes.Ok, "")
			}
			span.End()
			return err
		}
	}
}
