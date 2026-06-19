// Package otelutil holds OTel/OTLP boilerplate that is identical between the
// devshardd and decentralized-api observability packages. Per-process concerns
// (env-var names, exporter wiring, logger formatting) stay in the calling
// package.
//
// Why so small: keeping the shared surface minimal avoids forcing transitive
// imports (e.g. otlpmetricgrpc) into modules that do not need them.
package otelutil

import "strings"

// ParseHeaders accepts the standard OTLP "key1=value1,key2=value2" form and
// returns nil for empty/whitespace input. Malformed pairs are reported via the
// optional onMalformed callback and skipped (so a single typo does not abort
// init). Pass nil to silently ignore malformed pairs.
func ParseHeaders(raw string, onMalformed func(pair string)) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	out := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		key, value, found := strings.Cut(pair, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !found || key == "" || value == "" {
			if onMalformed != nil {
				onMalformed(pair)
			}
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
