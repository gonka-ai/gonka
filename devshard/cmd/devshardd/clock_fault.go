package main

import "net/http"

// wrapClockHandler returns 503 when fault reports true so citest can fail the
// gateway clock probe without taking down /healthz or inference.
func wrapClockHandler(inner http.Handler, fault func() bool) http.Handler {
	if inner == nil {
		inner = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fault != nil && fault() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		inner.ServeHTTP(w, r)
	})
}
