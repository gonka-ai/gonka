package api

import (
	"cmp"
	"net/http"
	"slices"

	"devshard/cmd/gateway/limits"
	"devshard/cmd/gateway/perf"
)

// HostStates is the routing side of a host: whether it is withheld, and how it has been performing.
type HostStates interface {
	Snapshot() []perf.HostState
	Degraded(participant, model string) bool
	Capability(participant, model string) (contextLimit, versionRefusals, toolRefusals, contextRefusals uint64)
}

// HostWindows is the admission side of a host: what it may take right now.
type HostWindows interface {
	Snapshot() []limits.HostWindow
}

type hostView struct {
	ParticipantKey        string  `json:"participant_key"`
	Model                 string  `json:"model"`
	Ejected               bool    `json:"ejected"`
	Degraded              bool    `json:"degraded"`
	Suspicious            bool    `json:"suspicious"`
	Inflight              int     `json:"inflight"`
	DecodeSecondsPerToken float64 `json:"decode_seconds_per_token"`
	InputWindowTokens     float64 `json:"input_window_tokens"`
	OutputWindowTokens    float64 `json:"output_window_tokens"`
	InflightInputTokens   int64   `json:"inflight_input_tokens"`
	InflightOutputTokens  int64   `json:"inflight_output_tokens"`
	Cutoff                string  `json:"cutoff"`
	BackoffCount          int     `json:"backoff_count"`
	Available             bool    `json:"available"`
	ContextLimit          uint64  `json:"context_limit"`
	VersionRefusals       uint64  `json:"version_refusals"`
	ToolRefusals          uint64  `json:"tool_refusals"`
	ContextRefusals       uint64  `json:"context_refusals"`
}

type hostKey struct {
	participant string
	model       string
}

// handleAdminHosts answers why a host is or is not taking work. See operations.md, "What a host answer carries".
func (s *Server) handleAdminHosts(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hosts": s.hostViews()})
}

func (s *Server) hostViews() []hostView {
	views := map[hostKey]*hostView{}
	viewFor := func(participant, model string) *hostView {
		key := hostKey{participant: participant, model: model}
		if held, known := views[key]; known {
			return held
		}
		view := &hostView{ParticipantKey: participant, Model: model}
		views[key] = view
		return view
	}

	if s.hostStates != nil {
		for _, state := range s.hostStates.Snapshot() {
			view := viewFor(state.Participant, state.Model)
			view.Ejected = state.Ejected
			view.Degraded = s.hostStates.Degraded(state.Participant, state.Model)
			view.Inflight = state.Inflight
			view.DecodeSecondsPerToken = state.TimePerOutputToken.Seconds()
			view.ContextLimit, view.VersionRefusals, view.ToolRefusals, view.ContextRefusals =
				s.hostStates.Capability(state.Participant, state.Model)
		}
	}
	if s.hostWindows != nil {
		for _, window := range s.hostWindows.Snapshot() {
			view := viewFor(window.Participant, window.Model)
			view.InputWindowTokens = window.InputWindowTokens
			view.OutputWindowTokens = window.OutputWindowTokens
			view.InflightInputTokens = window.InflightInputTokens
			view.InflightOutputTokens = window.InflightOutputTokens
			view.Cutoff = string(window.Cutoff)
			view.BackoffCount = window.BackoffCount
			view.Available = window.Available
		}
	}

	pinned := map[string]bool{}
	if s.suspicious != nil {
		for _, participant := range s.suspicious.List() {
			pinned[participant] = true
		}
	}

	answer := make([]hostView, 0, len(views))
	for key, view := range views {
		view.Suspicious = pinned[key.participant]
		answer = append(answer, *view)
	}
	slices.SortFunc(answer, func(first, second hostView) int {
		return cmp.Or(cmp.Compare(first.ParticipantKey, second.ParticipantKey), cmp.Compare(first.Model, second.Model))
	})
	return answer
}
