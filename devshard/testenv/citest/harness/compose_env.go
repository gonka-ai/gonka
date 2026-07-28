package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ComposeServiceEnv maps service name → resolved environment.
type ComposeServiceEnv map[string]map[string]string

// Env returns the environment of one service, failing if it is absent.
func (c ComposeServiceEnv) Env(t *testing.T, service string) map[string]string {
	t.Helper()
	env, ok := c[service]
	require.Truef(t, ok, "service %q not in compose config (have %v)", service, c.Services())
	return env
}

// Services lists the configured service names.
func (c ComposeServiceEnv) Services() []string {
	out := make([]string, 0, len(c))
	for name := range c {
		out = append(out, name)
	}
	return out
}

// ComposeEnvConfig renders `docker compose config` for the given compose files
// (relative to dir) and returns each service's resolved environment.
//
// The command runs with a minimal environment so host variables cannot leak
// into interpolation; hostEnv supplies whatever the compose files require.
// Skips the test when docker is unavailable.
func ComposeEnvConfig(t *testing.T, dir string, hostEnv map[string]string, files ...string) ComposeServiceEnv {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}
	require.NotEmpty(t, files, "at least one compose file required")

	args := []string{"compose"}
	for _, f := range files {
		require.FileExists(t, filepath.Join(dir, f))
		args = append(args, "-f", f)
	}
	args = append(args, "config", "--format", "json")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir
	cmd.Env = minimalComposeEnv(hostEnv)

	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoErrorf(t, err, "docker compose %s\n%s", strings.Join(args, " "), stderr.String())

	var doc struct {
		Services map[string]struct {
			Environment json.RawMessage `json:"environment"`
		} `json:"services"`
	}
	require.NoError(t, json.Unmarshal(out, &doc))

	cfg := make(ComposeServiceEnv, len(doc.Services))
	for name, svc := range doc.Services {
		env, err := parseComposeEnvironment(svc.Environment)
		require.NoErrorf(t, err, "service %s environment", name)
		cfg[name] = env
	}
	return cfg
}

// minimalComposeEnv keeps only what the docker CLI itself needs plus the
// caller-supplied variables.
func minimalComposeEnv(hostEnv map[string]string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
	if v := os.Getenv("DOCKER_HOST"); v != "" {
		env = append(env, "DOCKER_HOST="+v)
	}
	if v := os.Getenv("DOCKER_CONFIG"); v != "" {
		env = append(env, "DOCKER_CONFIG="+v)
	}
	for k, v := range hostEnv {
		env = append(env, k+"="+v)
	}
	return env
}

// parseComposeEnvironment accepts both compose renderings: a map of
// name → value (values may be null) and a list of "NAME=value" strings.
func parseComposeEnvironment(raw json.RawMessage) (map[string]string, error) {
	env := map[string]string{}
	if len(raw) == 0 || string(raw) == "null" {
		return env, nil
	}
	var asMap map[string]*string
	if err := json.Unmarshal(raw, &asMap); err == nil {
		for k, v := range asMap {
			if v == nil {
				env[k] = ""
				continue
			}
			env[k] = *v
		}
		return env, nil
	}
	var asList []string
	if err := json.Unmarshal(raw, &asList); err != nil {
		return nil, fmt.Errorf("unsupported environment encoding: %s", string(raw))
	}
	for _, item := range asList {
		k, v, _ := strings.Cut(item, "=")
		env[k] = v
	}
	return env, nil
}
