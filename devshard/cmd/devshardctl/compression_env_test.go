package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The grammar is boolvalue's; what is ours is which variable is read.
func TestCompressRequestBodiesFromEnv(t *testing.T) {
	require.Equal(t, "DEVSHARD_GATEWAY_COMPRESS_REQUEST_BODIES", envGatewayCompressRequestBodies,
		"the operator manual documents this name")

	for _, testCase := range []struct {
		raw  string
		want bool
	}{
		{"", false},
		{"false", false},
		{"on", true},
		{" True ", true},
	} {
		t.Run("value "+testCase.raw, func(t *testing.T) {
			t.Setenv(envGatewayCompressRequestBodies, testCase.raw)

			enabled, err := compressRequestBodiesFromEnv()

			require.NoError(t, err)
			require.Equal(t, testCase.want, enabled)
		})
	}
}

// A typo must not read as "off".
func TestCompressRequestBodiesFromEnv_RefusesAnUnrecognizedValue(t *testing.T) {
	t.Setenv(envGatewayCompressRequestBodies, "maybe")

	_, err := compressRequestBodiesFromEnv()

	require.Error(t, err)
}
