package types

import (
	"errors"
	"testing"
)

func TestParseProtocolVersion(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    ProtocolVersion
		wantErr bool
	}{
		{name: "empty defaults to v1", in: "", want: ProtocolV1},
		{name: "v1", in: "v1", want: ProtocolV1},
		{name: "v2", in: "v2", want: ProtocolV2},
		{name: "v3", in: "v3", want: ProtocolV3},
		{name: "v4", in: "v4", want: "4"},
		{name: "bare 4", in: "4", want: "4"},
		{name: "v4.1r5", in: "v4.1r5", want: "4.1r5"},
		{name: "legacy semver", in: "0.2.11", want: "0.2.11"},
		{name: "named runtime", in: "mainnet-canary", want: "mainnet-canary"},
		{name: "v only", in: "v", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseProtocolVersion(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseProtocolVersion(%q)=%q, want %q", tt.in, got, tt.want)
			}
		})
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
