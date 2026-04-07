package download

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func createTestZip(t *testing.T, binaryName, content string) ([]byte, string) {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "test-*.zip")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())

	w := zip.NewWriter(tmpFile)
	f, err := w.Create(binaryName)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte(content))
	w.Close()
	tmpFile.Close()

	data, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}

	h := sha256.Sum256(data)
	return data, hex.EncodeToString(h[:])
}

func TestDownload(t *testing.T) {
	zipData, hash := createTestZip(t, "myapp", "#!/bin/sh\necho hello")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(zipData)
	}))
	defer srv.Close()

	destDir := t.TempDir()
	err := Download(context.Background(), srv.URL, hash, destDir, "myapp")
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	binPath := filepath.Join(destDir, "myapp")
	info, err := os.Stat(binPath)
	if err != nil {
		t.Fatalf("stat binary: %v", err)
	}
	if info.Mode()&0111 == 0 {
		t.Error("binary is not executable")
	}

	content, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "#!/bin/sh\necho hello" {
		t.Errorf("content = %q", string(content))
	}
}

func TestDownload_HashMismatch(t *testing.T) {
	zipData, _ := createTestZip(t, "myapp", "content")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(zipData)
	}))
	defer srv.Close()

	destDir := t.TempDir()
	err := Download(context.Background(), srv.URL, "wrong_hash", destDir, "myapp")
	if err == nil {
		t.Fatal("expected error on hash mismatch")
	}
}

func TestDownload_BinaryNotInZip(t *testing.T) {
	zipData, hash := createTestZip(t, "other", "content")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(zipData)
	}))
	defer srv.Close()

	destDir := t.TempDir()
	err := Download(context.Background(), srv.URL, hash, destDir, "myapp")
	if err == nil {
		t.Fatal("expected error when binary not in zip")
	}
}

func TestDownload_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	destDir := t.TempDir()
	err := Download(context.Background(), srv.URL, "abc", destDir, "myapp")
	if err == nil {
		t.Fatal("expected error on 404")
	}
}
