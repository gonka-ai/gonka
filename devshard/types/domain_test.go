package types

import (
	"errors"
	"testing"
)

func TestParseProtocolVersion_DefaultsToV4(t *testing.T) {
	got, err := ParseProtocolVersion("")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != ProtocolV4 {
		t.Fatalf("expected empty protocol to default to %s, got %s", ProtocolV4, got)
	}
}

func TestParseProtocolVersion_AcceptsRouteStyleV1(t *testing.T) {
	got, err := ParseProtocolVersion("v1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != ProtocolV1 {
		t.Fatalf("expected v1 to normalize to %s, got %s", ProtocolV1, got)
	}
}

func TestParseProtocolVersion_AcceptsRouteStyleV2(t *testing.T) {
	got, err := ParseProtocolVersion("v2")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != ProtocolV2 {
		t.Fatalf("expected v2 to normalize to %s, got %s", ProtocolV2, got)
	}
}

func TestParseProtocolVersion_AcceptsRouteStyleV3(t *testing.T) {
	got, err := ParseProtocolVersion("v3")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != ProtocolV3 {
		t.Fatalf("expected v3 to normalize to %s, got %s", ProtocolV3, got)
	}
}

func TestParseProtocolVersion_AcceptsRouteStyleV4(t *testing.T) {
	got, err := ParseProtocolVersion("v4")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != ProtocolV4 {
		t.Fatalf("expected v4 to normalize to %s, got %s", ProtocolV4, got)
	}
}

func TestParseProtocolVersion_AcceptsRouteStyleV5(t *testing.T) {
	got, err := ParseProtocolVersion("v5")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != ProtocolV5 {
		t.Fatalf("expected v5 to normalize to %s, got %s", ProtocolV5, got)
	}
	got, err = ParseProtocolVersion("5")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != ProtocolV5 {
		t.Fatalf("expected 5 to normalize to %s, got %s", ProtocolV5, got)
	}
}

func TestProtocolVersionAtLeast(t *testing.T) {
	if !ProtocolVersionAtLeast(ProtocolV5, ProtocolV5) {
		t.Fatal("expected v5 >= v5")
	}
	if !ProtocolVersionAtLeast(ProtocolV5, ProtocolV4) {
		t.Fatal("expected v5 >= v4")
	}
	if ProtocolVersionAtLeast(ProtocolV4, ProtocolV5) {
		t.Fatal("expected v4 < v5")
	}
	if !ProtocolVersionAtLeast("v5", ProtocolV5) {
		t.Fatal("expected route-style v5 >= ProtocolV5")
	}
}

func TestParseProtocolVersion_RejectsOldProtocol(t *testing.T) {
	if _, err := ParseProtocolVersion("0.2.11"); err == nil {
		t.Fatal("expected old protocol to be rejected")
	}
}

func TestValidateGroup(t *testing.T) {
	tests := []struct {
		name    string
		group   []SlotAssignment
		wantErr error
	}{
		{
			name: "valid compact group 0..2",
			group: []SlotAssignment{
				{SlotID: 0, ValidatorAddress: "a"},
				{SlotID: 1, ValidatorAddress: "b"},
				{SlotID: 2, ValidatorAddress: "c"},
			},
			wantErr: nil,
		},
		{
			name: "valid single slot",
			group: []SlotAssignment{
				{SlotID: 0, ValidatorAddress: "a"},
			},
			wantErr: nil,
		},
		{
			name:    "empty group",
			group:   []SlotAssignment{},
			wantErr: ErrInvalidGroup,
		},
		{
			name: "non-compact gap",
			group: []SlotAssignment{
				{SlotID: 0, ValidatorAddress: "a"},
				{SlotID: 2, ValidatorAddress: "b"},
			},
			wantErr: ErrInvalidGroup,
		},
		{
			name: "duplicate slot ID",
			group: []SlotAssignment{
				{SlotID: 0, ValidatorAddress: "a"},
				{SlotID: 0, ValidatorAddress: "b"},
			},
			wantErr: ErrInvalidGroup,
		},
		{
			name: "compact but unsorted",
			group: []SlotAssignment{
				{SlotID: 1, ValidatorAddress: "b"},
				{SlotID: 0, ValidatorAddress: "a"},
				{SlotID: 2, ValidatorAddress: "c"},
			},
			wantErr: ErrInvalidGroup,
		},
		{
			name:    "exceeds MaxGroupSize",
			group:   makeOversizedGroup(),
			wantErr: ErrInvalidGroup,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateGroup(tt.group)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected %v, got %v", tt.wantErr, err)
			}
		})
	}
}

func makeOversizedGroup() []SlotAssignment {
	group := make([]SlotAssignment, MaxGroupSize+1)
	for i := range group {
		group[i] = SlotAssignment{SlotID: uint32(i), ValidatorAddress: "v"}
	}
	return group
}
