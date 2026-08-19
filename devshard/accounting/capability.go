package accounting

// HostCapability reports what a participant's build refused. The booleans keep the shape readers
// already parse, but their meaning is now historical: a host is never withheld from routing for a
// refusal, so true means "refused at least once this epoch", never "currently unusable". The counts
// beside them say how often, which is what tells a one-off apart from a build that refuses everything.
type HostCapability struct {
	ProtocolVersionUnsupported bool   `json:"protocol_version_unsupported,omitempty"`
	ToolChoiceUnsupported      bool   `json:"tool_choice_unsupported,omitempty"`
	ContextLimit               uint64 `json:"context_limit,omitempty"`
	VersionRefusals            uint64 `json:"version_refusals,omitempty"`
	ToolRefusals               uint64 `json:"tool_refusals,omitempty"`
	ContextRefusals            uint64 `json:"context_refusals,omitempty"`
}

func (c HostCapability) empty() bool {
	return !c.ProtocolVersionUnsupported && !c.ToolChoiceUnsupported && c.ContextLimit == 0 &&
		c.VersionRefusals == 0 && c.ToolRefusals == 0 && c.ContextRefusals == 0
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
