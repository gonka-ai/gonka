package accounting

// HostCapability reports what a participant's build has proven it cannot do. The counters already
// show that the gateway stopped sending to a host; this says why the host itself is unusable, which
// otherwise only exists in gateway logs.
type HostCapability struct {
	ProtocolVersionUnsupported bool   `json:"protocol_version_unsupported,omitempty"`
	ToolChoiceUnsupported      bool   `json:"tool_choice_unsupported,omitempty"`
	ContextLimit               uint64 `json:"context_limit,omitempty"`
}

func (c HostCapability) empty() bool {
	return !c.ProtocolVersionUnsupported && !c.ToolChoiceUnsupported && c.ContextLimit == 0
}

// CapabilityFunc answers for one model on one participant. Context length and tool support belong to
// the model; only the protocol version is a property of the host's build.
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
