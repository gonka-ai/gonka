//go:build linux

package netns

import (
	"errors"
	"io/fs"
	"os"
	"testing"
)

func TestSandboxRefusesTheDaemonsOwnNamespace(t *testing.T) {
	// act
	target, err := sandbox(os.Getpid())

	// assert
	if errors.Is(err, fs.ErrPermission) {
		t.Skipf("the host's namespace is not readable here: %v", err)
	}
	if err == nil {
		target.Close()
		t.Fatal("a pid in the daemon's own namespace was opened as a sandbox")
	}
	if !errors.Is(err, errNotSandbox) {
		t.Fatalf("err = %v, want the sandbox refused", err)
	}
}
