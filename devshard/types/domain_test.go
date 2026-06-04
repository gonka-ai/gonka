package types

import (
	"errors"
	"testing"
)

func TestParseProtocolVersion_DefaultsToV1(t *testing.T) {
	got, err := ParseProtocolVersion("")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != ProtocolV1 {
		t.Fatalf("expected empty protocol to default to %s, got %s", ProtocolV1, got)
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
