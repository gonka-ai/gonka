package loadtest

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
)

const (
	// versiondDataDir is VERSIOND_DATA_DIR in the gencompose template; versiond
	// passes {VERSIOND_DATA_DIR}/{version} as --data-dir and devshardd keeps payloads under {data-dir}/payloads.
	versiondDataDir = "/opt/versiond/data"
	// payloadSampleMin is the payload-files-are-zstd floor of payload files; payloadSampleMax caps the files copied out.
	payloadSampleMin = 20
	payloadSampleMax = 256
)

var zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}

// PayloadFileStats is the payload-files-are-zstd sample of one host's payload directory.
type PayloadFileStats struct {
	Files        int      `json:"files"`
	NotZstd      []string `json:"not_zstd,omitempty"`
	Compressed   int64    `json:"compressed_bytes"`
	Decompressed int64    `json:"decompressed_bytes"`
	SampleError  string   `json:"sample_error,omitempty"`
	Error        string   `json:"error,omitempty"`
}

// zstdPayloadHosts lists the hosts whose scenario env puts payloads in files only.
func zstdPayloadHosts(s Scenario) []string {
	var hosts []string
	for _, host := range s.Topology.Hosts {
		if strings.EqualFold(strings.TrimSpace(host.Env["DEVSHARD_STORAGE_MODE"]), "sqlite") {
			hosts = append(hosts, host.ID)
		}
	}
	sort.Strings(hosts)
	return hosts
}

// payloadSampleProbe tars up to $2 payload files under $1; a missing dir yields an empty stream.
const payloadSampleProbe = `cd "$1" 2>/dev/null || exit 0
find . -type f ! -name '*.tmp' | sort | head -n "$2" | xargs -r tar -cf -`

// samplePayloadFiles copies a sample of the host's payload files into dest
// with docker exec, then scans the copy.
func samplePayloadFiles(ctx context.Context, dir, project, compose, service, versionName, dest string) PayloadFileStats {
	payloadDir := path.Join(versiondDataDir, versionName, "payloads")
	cmd := exec.CommandContext(ctx, "docker", "compose", "-p", project, "-f", compose, "exec", "-T", service,
		"sh", "-c", payloadSampleProbe, "probe", payloadDir, strconv.Itoa(payloadSampleMax))
	cmd.Dir = dir
	output, err := probeCommandOutput(cmd, service)
	if err != nil {
		return PayloadFileStats{Error: err.Error()}
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return PayloadFileStats{Error: err.Error()}
	}
	if err := extractPayloadTar(strings.NewReader(output), dest); err != nil {
		return PayloadFileStats{Error: err.Error()}
	}
	return scanPayloadDir(dest)
}

// extractPayloadTar writes the regular files of a tar stream under dest and
// refuses entries that would land outside it.
func extractPayloadTar(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("payload sample tar: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		name := filepath.Clean(filepath.FromSlash(header.Name))
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("payload sample tar: entry %q escapes the sample dir", header.Name)
		}
		target := filepath.Join(dest, name)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(file, tr)
		if closeErr := file.Close(); copyErr == nil {
			copyErr = closeErr
		}
		if copyErr != nil {
			return copyErr
		}
	}
}

// storedPayload mirrors the JSON that common/storage/payloads writes per file.
type storedPayload struct {
	PromptPayload   []byte `json:"prompt_payload"`
	ResponsePayload []byte `json:"response_payload"`
}

// scanPayloadDir checks every payload file under dir for the zstd magic and
// decompresses it into the stored payload JSON. Plain files count their size on both sides.
func scanPayloadDir(dir string) PayloadFileStats {
	var stats PayloadFileStats
	decoder, err := zstd.NewReader(nil)
	if err != nil {
		return PayloadFileStats{Error: err.Error()}
	}
	defer decoder.Close()
	sampleErr := func(rel string, err error) {
		if stats.SampleError == "" {
			stats.SampleError = fmt.Sprintf("%s: %v", rel, err)
		}
	}
	walkErr := filepath.WalkDir(dir, func(p string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || strings.HasSuffix(p, ".tmp") {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		stats.Files++
		stats.Compressed += int64(len(body))
		plain := body
		if !bytes.HasPrefix(body, zstdMagic) {
			stats.NotZstd = append(stats.NotZstd, rel)
		} else if plain, err = decoder.DecodeAll(body, nil); err != nil {
			sampleErr(rel, err)
			return nil
		}
		stats.Decompressed += int64(len(plain))
		var payload storedPayload
		if err := json.Unmarshal(plain, &payload); err != nil {
			sampleErr(rel, err)
		} else if !json.Valid(payload.PromptPayload) || len(payload.ResponsePayload) == 0 {
			sampleErr(rel, errors.New("prompt_payload is not JSON or response_payload is empty"))
		}
		return nil
	})
	if walkErr != nil {
		stats.Error = walkErr.Error()
	}
	return stats
}
