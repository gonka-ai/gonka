package hostctl

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCommandKeepsSuccessfulStderrOutOfStdout(t *testing.T) {
	output, err := runCommand(
		context.Background(),
		"sh",
		"-c",
		`printf '{"state":"active"}'; printf 'ssh warning\n' >&2`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if output != `{"state":"active"}` {
		t.Fatalf("stdout = %q, want clean JSON", output)
	}
}

func TestRunCommandIncludesStderrInFailure(t *testing.T) {
	_, err := runCommand(
		context.Background(),
		"sh",
		"-c",
		`printf 'permission denied\n' >&2; exit 23`,
	)
	if err == nil {
		t.Fatal("failing command returned no error")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error = %v, want exec.ExitError", err)
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("error does not include stderr: %v", err)
	}
}

func TestSSHRemoteBuildsQuotedRemoteCommand(t *testing.T) {
	dir := t.TempDir()
	capturePath := filepath.Join(dir, "arguments")
	sshPath := filepath.Join(dir, "capture-ssh")
	script := `#!/bin/sh
: > "$CAPTURE_PATH"
for arg in "$@"; do
	printf '%s\n' "$arg" >> "$CAPTURE_PATH"
done
printf 'ok'
`
	if err := os.WriteFile(sshPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAPTURE_PATH", capturePath)

	remote := SSHRemote{
		Binary:  sshPath,
		Options: []string{"-F", "config path"},
	}
	output, err := remote.Run(
		context.Background(),
		"operator@example.test",
		"gonka-routerctl",
		"host add",
		"value'with quote",
		"$HOME; touch /tmp/nope",
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if output != "ok" {
		t.Fatalf("SSH output = %q, want %q", output, "ok")
	}

	data, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(args) < 3 {
		t.Fatalf("captured SSH arguments = %q, want destination and command", args)
	}
	gotTail := args[len(args)-3:]
	wantTail := []string{
		"operator@example.test",
		"--",
		`'gonka-routerctl' 'host add' 'value'"'"'with quote' '$HOME; touch /tmp/nope' ''`,
	}
	for i := range wantTail {
		if gotTail[i] != wantTail[i] {
			t.Fatalf("SSH argument %d = %q, want %q", i, gotTail[i], wantTail[i])
		}
	}

	joined := strings.Join(args, "\n")
	for _, option := range []string{
		"BatchMode=yes",
		"ConnectTimeout=10",
		"ServerAliveInterval=5",
		"ServerAliveCountMax=3",
	} {
		if !strings.Contains(joined, option) {
			t.Fatalf("SSH arguments do not contain %q:\n%s", option, joined)
		}
	}
}
