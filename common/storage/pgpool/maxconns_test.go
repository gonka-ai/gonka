package pgpool

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestConfigureMaxConns(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    int32
		wantErr bool
	}{
		{name: "default", want: DefaultMaxConns},
		{name: "explicit", value: "12", want: 12},
		{name: "trimmed", value: " 8 ", want: 8},
		{name: "zero", value: "0", wantErr: true},
		{name: "negative", value: "-1", wantErr: true},
		{name: "not an integer", value: "many", wantErr: true},
		{name: "overflow", value: "2147483648", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PG_POOL_MAX_CONNS", tt.value)
			cfg := &pgxpool.Config{}
			err := ConfigureMaxConns(cfg)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, cfg.MaxConns)
		})
	}
}
