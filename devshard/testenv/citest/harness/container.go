package harness

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ContainerID resolves a compose service to its container ID.
func (s *Stack) ContainerID(t *testing.T, service string) string {
	t.Helper()
	args := append([]string{"compose"}, s.composeFileArgs()...)
	args = append(args, "ps", "-q", service)
	cmd := exec.Command("docker", args...)
	cmd.Dir = s.WorkDir
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "resolve container ID for %s: %s", service, output)
	id := strings.TrimSpace(string(output))
	require.NotEmpty(t, id, "container ID for %s", service)
	return id
}
