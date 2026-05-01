package testenv_test

// Phase 11 — pin the contract every Dockerfile.* image must satisfy
// without spinning up a docker daemon. We assert the bits that
// historically rotted between branches:
//
//   - The build context (set by gencompose) is the devshard module
//     root, so the Go path passed to `go build` must resolve to a
//     real package under devshard/. Mismatched paths produced silent
//     "unknown package" failures in subnet-testenv, costing hours.
//   - Every runtime stage uses distroless/static, not alpine — Phase 11
//     explicitly trades the shell for a smaller, audit-friendly base.
//   - Every image declares an ENTRYPOINT and references its expected
//     binary, so renaming a cmd/ folder breaks the test instead of
//     the docker build six minutes into CI.
//
// The dev overlay (Dockerfile.dev) is exempt: it intentionally ships
// air + dlv on top of golang:alpine and is exercised by the Phase 12
// overlay tests.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type dockerfileSpec struct {
	name        string // file name under devshard/testenv/
	binaryName  string // basename of the produced binary inside the image
	goPackage   string // import path passed to `go build`, relative to the build context
	packageDir  string // location of that package on disk, relative to devshard/ (the build context)
	expectsData bool   // when true, /data must be pre-created in the runtime image
}

var dockerfileSpecs = []dockerfileSpec{
	{
		name:       "Dockerfile.mock-chain",
		binaryName: "mockchain",
		goPackage:  "./testenv/cmd/mockchain",
		packageDir: "testenv/cmd/mockchain",
	},
	{
		name:       "Dockerfile.height-sync",
		binaryName: "heightsyncd",
		goPackage:  "./testenv/cmd/heightsyncd",
		packageDir: "testenv/cmd/heightsyncd",
	},
	{
		name:        "Dockerfile.devshardd-testenv",
		binaryName:  "devshardd-testenv",
		goPackage:   "./testenv/cmd/devshardd-testenv",
		packageDir:  "testenv/cmd/devshardd-testenv",
		expectsData: true,
	},
	{
		name:       "Dockerfile.devshardctl",
		binaryName: "devshardctl",
		goPackage:  "./cmd/devshardctl",
		packageDir: "cmd/devshardctl",
	},
}

// devshardRoot returns the devshard module root (one level above
// testenv/) — the same directory gencompose passes as the docker
// build context.
func devshardRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Dir(wd)
}

func readDockerfile(t *testing.T, name string) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(wd, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(body)
}

func TestDockerfiles_BuildContextMatchesDevshardRoot(t *testing.T) {
	root := devshardRoot(t)
	for _, spec := range dockerfileSpecs {
		spec := spec
		t.Run(spec.name, func(t *testing.T) {
			// The Go path the Dockerfile passes to `go build` must
			// exist under devshard/, otherwise the docker build will
			// die after copying the entire context — slow and opaque.
			pkg := filepath.Join(root, spec.packageDir, "main.go")
			if _, err := os.Stat(pkg); err != nil {
				t.Fatalf("Dockerfile %s references package %s but %s is missing: %v",
					spec.name, spec.goPackage, pkg, err)
			}
		})
	}
}

func TestDockerfiles_StaticContract(t *testing.T) {
	// `go build` invocations are split with backslash continuations,
	// so the package path lives on a later line. (?s) lets `.` match
	// newlines; the lazy quantifier keeps the match anchored to the
	// nearest package token.
	goBuildRe := regexp.MustCompile(`(?s)go build.*?(\./\S+)`)
	entrypointRe := regexp.MustCompile(`(?m)^ENTRYPOINT\s+\["([^"]+)"\]`)

	for _, spec := range dockerfileSpecs {
		spec := spec
		t.Run(spec.name, func(t *testing.T) {
			body := readDockerfile(t, spec.name)

			// Phase 11 requires multi-stage with a Go build stage.
			if !strings.Contains(body, "FROM golang:1.24-alpine AS build") {
				t.Errorf("%s: expected `FROM golang:1.24-alpine AS build`", spec.name)
			}

			// Runtime stage is distroless/static — explicit so a stray
			// `FROM alpine:` regression is caught.
			if !strings.Contains(body, "FROM gcr.io/distroless/static-debian12") {
				t.Errorf("%s: runtime stage must be distroless/static-debian12", spec.name)
			}
			if strings.Contains(body, "FROM alpine:") {
				t.Errorf("%s: alpine runtime stages were retired in Phase 11", spec.name)
			}

			// Static binaries — CGO would silently pull glibc back in
			// and break the distroless runtime.
			if !strings.Contains(body, "CGO_ENABLED=0") {
				t.Errorf("%s: must build with CGO_ENABLED=0 for distroless/static", spec.name)
			}

			// The build path must match the Go package gencompose
			// expects — the failure mode is otherwise extremely loud
			// at runtime, not at build time.
			m := goBuildRe.FindStringSubmatch(body)
			if m == nil {
				t.Fatalf("%s: missing `go build ./...` invocation", spec.name)
			}
			if m[1] != spec.goPackage {
				t.Errorf("%s: go build target = %q, want %q (build context is devshard/)",
					spec.name, m[1], spec.goPackage)
			}

			// ENTRYPOINT must be the well-known binary path so the
			// compose `command:` overrides land where operators
			// expect them.
			ep := entrypointRe.FindStringSubmatch(body)
			if ep == nil {
				t.Fatalf("%s: missing ENTRYPOINT", spec.name)
			}
			wantEntry := "/usr/local/bin/" + spec.binaryName
			if ep[1] != wantEntry {
				t.Errorf("%s: ENTRYPOINT = %q, want %q", spec.name, ep[1], wantEntry)
			}

			// devshardd-testenv must pre-create /data — distroless
			// has no shell, so we can't `mkdir -p` from an init
			// script and SQLite fails silently if the path is missing.
			if spec.expectsData {
				if !strings.Contains(body, "/data") || !strings.Contains(body, "mkdir -p /out/data") {
					t.Errorf("%s: must pre-create /data so SQLite store boots cleanly", spec.name)
				}
				if !strings.Contains(body, "ENV DATA_DIR=/data") {
					t.Errorf("%s: missing ENV DATA_DIR=/data default", spec.name)
				}
			}
		})
	}
}

func TestDockerfiles_DevOverlayUntouched(t *testing.T) {
	// Phase 12's dev overlay deliberately keeps alpine + air + dlv —
	// touching it from the Phase 11 distroless work would silently
	// kill live-reload, so we pin the contract here too.
	body := readDockerfile(t, "Dockerfile.dev")
	for _, want := range []string{
		"FROM golang:1.24-alpine",
		"go install github.com/air-verse/air",
		"go install github.com/go-delve/delve/cmd/dlv",
		`ENTRYPOINT ["air"]`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Dockerfile.dev no longer contains %q", want)
		}
	}
}
