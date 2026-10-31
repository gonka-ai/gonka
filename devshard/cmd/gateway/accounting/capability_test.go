package accounting

import "testing"

// Test flow:
//  1. Build two participant records, one refused and one clean.
//  2. Attach capabilities using a lookup that flags only the refused participant.
//  3. Assert the refused record carries the verdict and its refusal count.
//  4. Assert the clean record carries no capability block.
func TestCapabilityAttachesOnlyWhenSomethingIsKnown(t *testing.T) {
	records := []ParticipantRecord{{Participant: "refused"}, {Participant: "clean"}}

	attachCapabilities(records, func(participant, _ string) HostCapability {
		if participant == "refused" {
			return HostCapability{ProtocolVersionUnsupported: true, VersionRefusals: 3, ContextLimit: 8192}
		}
		return HostCapability{}
	})

	if records[0].Capability == nil {
		t.Fatal("a host that refused carries no capability block")
	}
	if !records[0].Capability.ProtocolVersionUnsupported || records[0].Capability.VersionRefusals != 3 {
		t.Errorf("capability = %+v, want the verdict and its count", records[0].Capability)
	}
	if records[1].Capability != nil {
		t.Errorf("a host with nothing wrong carries %+v, want nothing", records[1].Capability)
	}
}

// Test flow:
//  1. Build one participant record.
//  2. Attach capabilities with a nil lookup function.
//  3. Assert the record's capability block stays nil.
func TestCapabilityNoLookupLeavesRecordsUntouched(t *testing.T) {
	records := []ParticipantRecord{{Participant: "any"}}

	attachCapabilities(records, nil)

	if records[0].Capability != nil {
		t.Error("a gateway with no perf tracker still attached a capability block")
	}
}

// Test flow:
//  1. Build one participant record.
//  2. Attach capabilities using a lookup that reports refusals but a lapsed verdict.
//  3. Assert the capability block still carries the refusal total.
//  4. Assert the expired verdict is not reported as currently blocking.
func TestCapabilityStillReportsAHostWhoseVerdictExpired(t *testing.T) {
	records := []ParticipantRecord{{Participant: "recovered"}}

	attachCapabilities(records, func(string, string) HostCapability {
		return HostCapability{VersionRefusals: 2}
	})

	if records[0].Capability == nil {
		t.Fatal("the refusal total vanished with the verdict that expired")
	}
	if records[0].Capability.ProtocolVersionUnsupported {
		t.Error("an expired verdict is still reported as blocking")
	}
}

// Test flow:
//  1. Build one participant record.
//  2. Attach capabilities using a lookup that reports only output overruns.
//  3. Assert the record carries a capability block with the overrun count.
func TestCapabilityReportsAHostThatOverranItsReservation(t *testing.T) {
	records := []ParticipantRecord{{Participant: "overran"}}

	attachCapabilities(records, func(string, string) HostCapability {
		return HostCapability{OutputOverruns: 4}
	})

	if records[0].Capability == nil || records[0].Capability.OutputOverruns != 4 {
		t.Fatalf("capability = %+v, want the four overruns", records[0].Capability)
	}
}
