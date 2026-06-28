package observability

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Service is the entry point used by call sites in this process. It avoids
// exposing OTel/Prometheus types directly so that we can change the underlying
// instrumentation without rewriting every call site.
//
// A nil-safe singleton (Default) is provided for code paths that don't have
// dependency injection wired through them. Tests can construct alternative
// instances if they need to inspect emitted spans.
type Service struct {
	inference *InferenceTracer
	chain     *ChainTracer
}

// Default is the process-wide Service instance, lazily wired against the
// global OTel/Prometheus providers.
var Default = NewService()

// Inference is a shorthand for Default.Inference().
var Inference = Default.Inference()

// Chain is a shorthand for Default.Chain().
var Chain = Default.Chain()

// NewService constructs a fresh Service. Useful for tests that swap providers
// per case; production code should usually use Default.
func NewService() *Service {
	return &Service{
		inference: &InferenceTracer{},
		chain:     &ChainTracer{},
	}
}

// Inference exposes the inference-flow tracer.
func (s *Service) Inference() *InferenceTracer {
	if s == nil {
		return (&Service{inference: &InferenceTracer{}}).inference
	}
	return s.inference
}

// Chain exposes the chain-interaction tracer.
func (s *Service) Chain() *ChainTracer {
	if s == nil {
		return (&Service{chain: &ChainTracer{}}).chain
	}
	return s.chain
}

// strAttr is a tiny helper that drops empty values so we don't pollute spans
// with `attr=""` noise.
func strAttr(key, value string) (attribute.KeyValue, bool) {
	if value == "" {
		return attribute.KeyValue{}, false
	}
	return attribute.String(key, value), true
}

// appendStr appends a string attribute only if value is non-empty.
func appendStr(dst []attribute.KeyValue, key, value string) []attribute.KeyValue {
	if attr, ok := strAttr(key, value); ok {
		return append(dst, attr)
	}
	return dst
}

// modelMetric returns the per-call metric attribute set for a given model.
// Centralised so every span that carries a model gets the same metric label.
func modelMetric(model string) []attribute.KeyValue {
	if model == "" {
		return nil
	}
	return []attribute.KeyValue{attribute.String("model", model)}
}

// kindOrInternal narrows the SpanKind to a default when none is specified.
func kindOrInternal(k trace.SpanKind) trace.SpanKind {
	if k == trace.SpanKindUnspecified {
		return trace.SpanKindInternal
	}
	return k
}
