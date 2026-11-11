package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDevshardGatewayComposeHealthcheckMatchesRuntimeImage(t *testing.T) {
	composePath := filepath.Join("..", "..", "..", "deploy", "join", "docker-compose.devshard-gateway.yml")
	composeBytes, err := os.ReadFile(composePath)
	require.NoError(t, err)
	composeText := string(composeBytes)

	dockerfilePath := filepath.Join("..", "..", "Dockerfile")
	dockerfileBytes, err := os.ReadFile(dockerfilePath)
	require.NoError(t, err)
	dockerfileText := string(dockerfileBytes)

	// Extract devshardctl-runtime stage from devshard/Dockerfile.
	stageIdx := strings.Index(dockerfileText, "AS devshardctl-runtime")
	require.NotEqual(t, -1, stageIdx, "devshardctl-runtime stage must exist in devshard/Dockerfile")
	stageRest := dockerfileText[stageIdx:]
	if nextFrom := strings.Index(stageRest[1:], "\nFROM "); nextFrom != -1 {
		stageRest = stageRest[:nextFrom+1]
	}

	require.Contains(t, composeText, "/v1/status")
	require.Contains(t, composeText, `"wget"`, "gateway compose healthcheck should use wget present in devshardctl-runtime")
	require.Contains(t, stageRest, "wget", "devshardctl-runtime image must install wget for gateway healthcheck")
	require.NotContains(t, composeText, `"curl"`, "devshardctl-runtime does not ship curl; using curl leaves container permanently unhealthy")
}
