package main

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDockerBuildPassesVersionMetadata(t *testing.T) {
	makefile, err := os.ReadFile("Makefile")
	require.NoError(t, err)
	require.Equal(t, 2, strings.Count(string(makefile), "\n\t\t--build-arg LDFLAGS='$(ldflags)' \\"))

	dockerfile, err := os.ReadFile("Dockerfile")
	require.NoError(t, err)
	require.Contains(t, string(dockerfile), "\nARG LDFLAGS\n")
	require.Contains(t, string(dockerfile), `-ldflags "$LDFLAGS"`)
}
