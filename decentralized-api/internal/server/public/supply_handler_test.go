package public

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChainRESTURLFromRPCURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "local rpc port",
			in:   "http://node:26657",
			want: "http://node:1317",
		},
		{
			name: "public chain rpc path",
			in:   "http://node1.gonka.ai:8000/chain-rpc/",
			want: "http://node1.gonka.ai:8000/chain-api",
		},
		{
			name: "empty defaults local rest",
			in:   "",
			want: "http://localhost:1317",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := chainRESTURLFromRPCURL(tt.in)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestFormatNgonkaAsGonka(t *testing.T) {
	got, err := formatNgonkaAsGonka("90284028887366231")
	require.NoError(t, err)
	require.Equal(t, "90284028.887366231", got)

	got, err = formatNgonkaAsGonka("1000000000")
	require.NoError(t, err)
	require.Equal(t, "1.000000000", got)
}

func TestFetchTotalSupplyGonka(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/cosmos/bank/v1beta1/supply/by_denom", r.URL.Path)
		require.Equal(t, "ngonka", r.URL.Query().Get("denom"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"amount":{"denom":"ngonka","amount":"474030050545505610"}}`))
	}))
	defer server.Close()

	got, err := fetchTotalSupplyGonka(context.Background(), server.Client(), server.URL)
	require.NoError(t, err)
	require.Equal(t, "474030050.545505610", got)
}
