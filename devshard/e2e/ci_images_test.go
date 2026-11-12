package e2e

import (
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Test flow:
//  1. Read every local image the e2e compose file defaults to.
//  2. Read the image tags the CI workflow's `test-devshard-e2e` job builds before it runs `make e2e-test`.
//  3. Assert each local image the compose file starts is one the job builds.
func TestTheCIE2EJobBuildsEveryLocalImageTheStackStarts(t *testing.T) {
	compose, err := os.ReadFile("../docker-compose.e2e.yml")
	require.NoError(t, err)
	matches := regexp.MustCompile(`DEVSHARD_E2E_[A-Z_]+_IMAGE:-([^}]+:local)\}`).FindAllSubmatch(compose, -1)
	wanted := make([]string, 0, len(matches))
	for _, match := range matches {
		wanted = append(wanted, string(match[1]))
	}
	require.NotEmpty(t, wanted)

	raw, err := os.ReadFile("../../.github/workflows/verify.yml")
	require.NoError(t, err)
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &workflow))
	job, ok := workflow.Jobs["test-devshard-e2e"]
	require.True(t, ok, "verify.yml has no test-devshard-e2e job")
	var built []string
	for _, step := range job.Steps {
		if tags := step.With["tags"]; tags != "" {
			built = append(built, tags)
		}
	}

	for _, image := range wanted {
		require.True(t, slices.Contains(built, image), "the e2e job starts %s but builds only %v", image, built)
	}
}
