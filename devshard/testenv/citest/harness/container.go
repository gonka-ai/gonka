package harness

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ContainerID resolves a compose service to its container ID.
func (s *Stack) ContainerID(t *testing.T, service string) string {
	t.Helper()
	id, err := s.containerID(service)
	require.NoError(t, err)
	return id
}

func (s *Stack) containerID(service string) (string, error) {
	args := append([]string{"compose"}, s.composeFileArgs()...)
	args = append(args, "ps", "-q", service)
	cmd := exec.Command("docker", args...)
	cmd.Dir = s.WorkDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("resolve container ID for %s: %w: %s", service, err, output)
	}
	ids := strings.Fields(string(output))
	if len(ids) != 1 {
		return "", fmt.Errorf("resolve container ID for %s: got %d IDs", service, len(ids))
	}
	return ids[0], nil
}

func containerExitCode(containerID string) (int, error) {
	cmd := exec.Command("docker", "inspect", "--format", "{{.State.ExitCode}}", containerID)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("inspect exit code for container %s: %w: %s", containerID, err, output)
	}
	exitCode, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		return 0, fmt.Errorf("parse exit code for container %s: %w", containerID, err)
	}
	return exitCode, nil
}
