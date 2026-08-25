package configenv

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseBool(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    bool
		wantErr bool
	}{
		{name: "empty", raw: "", want: false},
		{name: "zero", raw: "0", want: false},
		{name: "f", raw: "f", want: false},
		{name: "false", raw: "false", want: false},
		{name: "no", raw: "no", want: false},
		{name: "off", raw: "off", want: false},
		{name: "one", raw: "1", want: true},
		{name: "t", raw: "t", want: true},
		{name: "true", raw: "true", want: true},
		{name: "yes", raw: "yes", want: true},
		{name: "on", raw: "on", want: true},
		{name: "case and whitespace", raw: "  YeS\t", want: true},
		{name: "false case and whitespace", raw: "  OFF\n", want: false},
		{name: "number outside grammar", raw: "2", wantErr: true},
		{name: "unknown word", raw: "enabled", wantErr: true},
		{name: "internal whitespace", raw: "t rue", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseBool(tt.raw)
			if tt.wantErr {
				require.Error(t, err)
				require.False(t, got)
				require.Contains(t, err.Error(), "invalid boolean value")
				require.Contains(t, err.Error(), "for false or")
				require.Contains(t, err.Error(), tt.raw)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
