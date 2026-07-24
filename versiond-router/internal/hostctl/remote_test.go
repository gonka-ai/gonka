package hostctl

import (
	"context"
	"errors"
	"os/exec"
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
