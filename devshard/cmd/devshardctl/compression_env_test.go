package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRetiredCompressRequestBodiesEnvIsIgnored(t *testing.T) {
	require.Equal(t, "DEVSHARD_GATEWAY_COMPRESS_REQUEST_BODIES", envGatewayCompressRequestBodies)
	t.Setenv(envGatewayCompressRequestBodies, "maybe")
	noteRetiredCompressRequestBodies()
	t.Setenv(envGatewayCompressRequestBodies, "on")
	noteRetiredCompressRequestBodies()
}
