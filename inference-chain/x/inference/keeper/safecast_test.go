package keeper

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSafeInt32FromInt64(t *testing.T) {
	tests := []struct {
		name    string
		input   int64
		want    int32
		wantErr bool
	}{
		{"zero", 0, 0, false},
		{"one", 1, 1, false},
		{"negative one", -1, -1, false},
		{"MaxInt32", math.MaxInt32, math.MaxInt32, false},
		{"MinInt32", math.MinInt32, math.MinInt32, false},
		{"MaxInt32+1", math.MaxInt32 + 1, 0, true},
		{"MinInt32-1", math.MinInt32 - 1, 0, true},
		{"MaxInt64", math.MaxInt64, 0, true},
		{"MinInt64", math.MinInt64, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := safeInt32FromInt64(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				require.Equal(t, int32(0), got)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
			}
		})
	}
}

func TestSafeUint32FromInt64(t *testing.T) {
	tests := []struct {
		name    string
		input   int64
		want    uint32
		wantErr bool
	}{
		{"zero", 0, 0, false},
		{"one", 1, 1, false},
		{"negative one", -1, 0, true},
		{"MaxUint32", math.MaxUint32, math.MaxUint32, false},
		{"MaxUint32+1", math.MaxUint32 + 1, 0, true},
		{"MaxInt64", math.MaxInt64, 0, true},
		{"negative MaxInt64", -math.MaxInt64, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := safeUint32FromInt64(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				require.Equal(t, uint32(0), got)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
			}
		})
	}
}

func TestSafeUint32FromUint64(t *testing.T) {
	tests := []struct {
		name    string
		input   uint64
		want    uint32
		wantErr bool
	}{
		{"zero", 0, 0, false},
		{"one", 1, 1, false},
		{"MaxUint32", math.MaxUint32, math.MaxUint32, false},
		{"MaxUint32+1", math.MaxUint32 + 1, 0, true},
		{"MaxUint64", math.MaxUint64, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := safeUint32FromUint64(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				require.Equal(t, uint32(0), got)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
			}
		})
	}
}

func TestSafeInt32FromUint64(t *testing.T) {
	tests := []struct {
		name    string
		input   uint64
		want    int32
		wantErr bool
	}{
		{"zero", 0, 0, false},
		{"one", 1, 1, false},
		{"MaxInt32", math.MaxInt32, math.MaxInt32, false},
		{"MaxInt32+1", math.MaxInt32 + 1, 0, true},
		{"MaxUint64", math.MaxUint64, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := safeInt32FromUint64(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				require.Equal(t, int32(0), got)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
			}
		})
	}
}

func TestSafeUint8FromUint32(t *testing.T) {
	tests := []struct {
		name    string
		input   uint32
		want    uint8
		wantErr bool
	}{
		{"zero", 0, 0, false},
		{"one", 1, 1, false},
		{"255", 255, 255, false},
		{"256", 256, 0, true},
		{"MaxUint32", math.MaxUint32, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := safeUint8FromUint32(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				require.Equal(t, uint8(0), got)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
			}
		})
	}
}

func TestClampInt32FromInt(t *testing.T) {
	var warnings []string
	logWarn := func(msg string, keyvals ...any) {
		warnings = append(warnings, msg)
	}

	tests := []struct {
		name        string
		input       int
		want        int32
		wantWarning bool
	}{
		{"zero", 0, 0, false},
		{"MaxInt32", math.MaxInt32, math.MaxInt32, false},
		{"MaxInt32+1", math.MaxInt32 + 1, math.MaxInt32, true},
		{"large value", math.MaxInt64, math.MaxInt32, true},
		{"MinInt32", math.MinInt32, math.MinInt32, false},
		{"MinInt32-1", math.MinInt32 - 1, math.MinInt32, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warnings = nil
			got := clampInt32FromInt(tt.input, logWarn)
			require.Equal(t, tt.want, got)
			if tt.wantWarning {
				require.NotEmpty(t, warnings, "expected warning log")
			} else {
				require.Empty(t, warnings, "expected no warning log")
			}
		})
	}

	// Test with nil logWarn (should not panic)
	t.Run("nil_logWarn_no_panic", func(t *testing.T) {
		got := clampInt32FromInt(math.MaxInt64, nil)
		require.Equal(t, int32(math.MaxInt32), got)
	})
}
