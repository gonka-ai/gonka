package health

import (
	"context"
	"encoding/json"
	"net/http"
)

type StatusEntry struct {
	Name              string `json:"name"`
	Port              int    `json:"port"`
	Status            string `json:"status"`
	SHA256            string `json:"sha256,omitempty"`
	BinaryVersion     string `json:"binary_version,omitempty"`
	ProxyInflight     int64  `json:"proxy_inflight,omitempty"`
	LifecycleInflight int64  `json:"lifecycle_inflight,omitempty"`
	InflightKnown     bool   `json:"inflight_known,omitempty"`
}

type Summary struct {
	SchemaVersion     int           `json:"schema_version"`
	State             string        `json:"state"`
	Ready             bool          `json:"ready"`
	Accepting         bool          `json:"accepting"`
	ProxyInflight     int64         `json:"proxy_inflight"`
	LifecycleInflight int64         `json:"lifecycle_inflight"`
	Inflight          int64         `json:"inflight"`
	InflightKnown     bool          `json:"inflight_known"`
	Idle              bool          `json:"idle"`
	Children          []StatusEntry `json:"children"`
}

type SummaryFunc func(context.Context) Summary

// Handler returns an http.HandlerFunc that writes the health status as JSON.
func Handler(statusFn func() []StatusEntry, summaryFn ...SummaryFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("summary") == "1" && len(summaryFn) > 0 {
			_ = json.NewEncoder(w).Encode(summaryFn[0](r.Context()))
			return
		}
		_ = json.NewEncoder(w).Encode(statusFn())
	}
}

func BuildSummary(
	state string,
	accepting bool,
	proxyInflight int64,
	children []StatusEntry,
) Summary {
	lifecycleInflight := int64(0)
	inflightKnown := true
	for _, child := range children {
		lifecycleInflight += child.LifecycleInflight
		if !child.InflightKnown {
			inflightKnown = false
		}
	}
	inflight := proxyInflight
	if lifecycleInflight > inflight {
		inflight = lifecycleInflight
	}
	ready := state == "serving" && accepting && childrenReady(children)
	return Summary{
		SchemaVersion:     1,
		State:             state,
		Ready:             ready,
		Accepting:         accepting,
		ProxyInflight:     proxyInflight,
		LifecycleInflight: lifecycleInflight,
		Inflight:          inflight,
		InflightKnown:     inflightKnown,
		Idle:              inflightKnown && inflight == 0,
		Children:          children,
	}
}

func childrenReady(children []StatusEntry) bool {
	running := 0
	for _, child := range children {
		switch child.Status {
		case "running":
			running++
		case "draining":
		default:
			return false
		}
	}
	return running > 0
}
