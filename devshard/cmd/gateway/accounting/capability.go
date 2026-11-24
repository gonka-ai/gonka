package accounting

// The window half is this process's live congestion state, not the epoch's history: it is read at query
// time and a restart starts it over. See docs/accounting.md, "What a host is allowed to be doing".
type HostCapability struct {
	ProtocolVersionUnsupported bool   `json:"protocol_version_unsupported,omitempty"`
	ToolChoiceUnsupported      bool   `json:"tool_choice_unsupported,omitempty"`
	ContextLimit               uint64 `json:"context_limit,omitempty"`
	VersionRefusals            uint64 `json:"version_refusals,omitempty"`
	ToolRefusals               uint64 `json:"tool_refusals,omitempty"`
	ContextRefusals            uint64 `json:"context_refusals,omitempty"`
	OutputOverruns             uint64 `json:"output_overruns,omitempty"`

	InputWindowTokens    uint64 `json:"input_window_tokens,omitempty"`
	OutputWindowTokens   uint64 `json:"output_window_tokens,omitempty"`
	InflightInputTokens  uint64 `json:"window_inflight_input_tokens,omitempty"`
	InflightOutputTokens uint64 `json:"window_inflight_output_tokens,omitempty"`
	WindowCutoff         string `json:"window_cutoff,omitempty"`
	WindowWeight         uint64 `json:"window_weight,omitempty"`
}

func (c HostCapability) empty() bool {
	return !c.ProtocolVersionUnsupported && !c.ToolChoiceUnsupported && c.ContextLimit == 0 &&
		c.VersionRefusals == 0 && c.ToolRefusals == 0 && c.ContextRefusals == 0 && c.OutputOverruns == 0 &&
		c.InputWindowTokens == 0 && c.OutputWindowTokens == 0 &&
		c.InflightInputTokens == 0 && c.InflightOutputTokens == 0 &&
		c.WindowCutoff == "" && c.WindowWeight == 0
}

type CapabilityFunc func(participant, model string) HostCapability

func attachCapabilities(records []ParticipantRecord, lookup CapabilityFunc) {
	if lookup == nil {
		return
	}
	for i := range records {
		if capability := lookup(records[i].Participant, records[i].Model); !capability.empty() {
			records[i].Capability = &capability
		}
	}
}
