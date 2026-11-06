package observability_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// payloadLevelAssignment captures the effective value of a DEVSHARD_LOG_PAYLOADS
// assignment, whether written literally or as a ${VAR:-default} substitution.
// The trailing [:=] rules out the DEVSHARD_LOG_PAYLOADS_* siblings.
var payloadLevelAssignment = regexp.MustCompile(
	`DEVSHARD_LOG_PAYLOADS\s*[:=]\s*(?:\$\{DEVSHARD_LOG_PAYLOADS:-([A-Za-z]*)\}|"?([A-Za-z]+)"?)`)

// TestDeployNeverEnablesPayloadCapture guards the T4a privacy rule: production
// compose/env templates must leave DEVSHARD_LOG_PAYLOADS at off. Any other
// level puts request and response bodies into operator-visible logs, so the
// check covers redacted and hash too, not just full. It walks deploy/
// recursively because the tree has nested compose and override files.
func TestDeployNeverEnablesPayloadCapture(t *testing.T) {
	root := filepath.Join("..", "..", "deploy")

	var hits []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !isDeployConfigFile(d.Name()) {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			for _, m := range payloadLevelAssignment.FindAllStringSubmatch(line, -1) {
				level := strings.ToLower(m[1] + m[2])
				if level != "" && level != "off" {
					hits = append(hits, path+": "+strings.TrimSpace(line))
				}
			}
		}
		return nil
	})
	require.NoError(t, err)
	require.Empty(t, hits, "deploy/ must leave DEVSHARD_LOG_PAYLOADS=off:\n%s", strings.Join(hits, "\n"))
}

func isDeployConfigFile(name string) bool {
	switch filepath.Ext(name) {
	case ".yml", ".yaml", ".env", ".template", ".conf", ".sh":
		return true
	}
	return strings.Contains(name, "env")
}

// TestPayloadLevelAssignmentMatcher pins the matcher itself, since a silent
// regex failure would make the guard above pass on everything.
func TestPayloadLevelAssignmentMatcher(t *testing.T) {
	for _, tc := range []struct {
		line string
		want string
	}{
		{"      DEVSHARD_LOG_PAYLOADS: ${DEVSHARD_LOG_PAYLOADS:-off}", "off"},
		{"      DEVSHARD_LOG_PAYLOADS: ${DEVSHARD_LOG_PAYLOADS:-full}", "full"},
		{"DEVSHARD_LOG_PAYLOADS=full", "full"},
		{`      DEVSHARD_LOG_PAYLOADS: "redacted"`, "redacted"},
		{"      DEVSHARD_LOG_PAYLOADS: hash", "hash"},
		{"      DEVSHARD_LOG_PAYLOADS_MLNODE: true", ""},
		{"      DEVSHARD_LOG_PAYLOADS_MAX_BYTES: ${DEVSHARD_LOG_PAYLOADS_MAX_BYTES:-16384}", ""},
	} {
		m := payloadLevelAssignment.FindStringSubmatch(tc.line)
		var got string
		if m != nil {
			got = strings.ToLower(m[1] + m[2])
		}
		require.Equal(t, tc.want, got, tc.line)
	}
}
