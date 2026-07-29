package harness

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func (s *Stack) BuildHostctl(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(s.WorkDir, "gonka-hostctl")
	routerDir := filepath.Join(s.TestenvDir, "..", "..", "versiond-router")
	cmd := exec.Command("go", "build", "-o", binary, "./cmd/gonka-hostctl")
	cmd.Dir = routerDir
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "build gonka-hostctl: %s", output)
	return binary
}

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

func (s *Stack) RunHostctl(
	ctx context.Context,
	binary string,
	mode string,
	args ...string,
) (string, error) {
	commandArgs := append([]string{mode}, args...)
	cmd := exec.CommandContext(ctx, binary, commandArgs...)
	cmd.Dir = s.WorkDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("gonka-hostctl %s: %w: %s", mode, err, output)
	}
	return string(output), nil
}
