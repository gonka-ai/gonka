package loadtest

import (
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"devshard/testenv/config"
)

const (
	buildFileName     = "build.json"
	testenvBuildTag   = "devshard_testenv"
	testenvEnvPrefix  = "DEVSHARD_TESTENV_"
	tripfileEventKind = "tripfile"
)

// Build names prebuilt artifacts under test. The stack runs them instead of
// building devshardd, versiond, versiond-router and the gateway from the checkout.
type Build struct {
	Name          string      `yaml:"name" json:"name"`
	Protocol      string      `yaml:"protocol" json:"protocol"`
	BinaryVersion string      `yaml:"binary_version" json:"binary_version"`
	Devshardd     string      `yaml:"devshardd" json:"devshardd"`
	Images        BuildImages `yaml:"images" json:"images"`
}

type BuildImages struct {
	Versiond       string `yaml:"versiond" json:"versiond"`
	VersiondRouter string `yaml:"versiond_router" json:"versiond_router"`
	Gateway        string `yaml:"gateway" json:"gateway"`
}

// BuildRecord is build.json: what the run actually ran, by content.
type BuildRecord struct {
	Build
	DevsharddSHA256 string            `json:"devshardd_sha256"`
	DevsharddTags   string            `json:"devshardd_tags"`
	ImageIDs        map[string]string `json:"image_ids"`
}

// LoadBuild reads a build file; a relative devshardd path is relative to the file.
func LoadBuild(path string) (*Build, error) {
	var build Build
	if err := loadYAML(path, &build); err != nil {
		return nil, err
	}
	if build.Devshardd != "" && !filepath.IsAbs(build.Devshardd) {
		dir, err := filepath.Abs(filepath.Dir(path))
		if err != nil {
			return nil, err
		}
		build.Devshardd = filepath.Join(dir, build.Devshardd)
	}
	for _, field := range []struct{ name, value string }{
		{"name", build.Name},
		{"protocol", build.Protocol},
		{"binary_version", build.BinaryVersion},
		{"devshardd", build.Devshardd},
		{"images.versiond", build.Images.Versiond},
		{"images.versiond_router", build.Images.VersiondRouter},
		{"images.gateway", build.Images.Gateway},
	} {
		if strings.TrimSpace(field.value) == "" {
			return nil, fmt.Errorf("build %s: %s is required", path, field.name)
		}
	}
	return &build, nil
}

func (b *Build) apply(cfg *config.File) {
	cfg.Versiond.VersionName = b.Protocol
	cfg.Versiond.BinaryVersion = b.BinaryVersion
	cfg.Versiond.HostBinaryMount = b.Devshardd
	cfg.Versiond.Image = b.Images.Versiond
	cfg.VersiondRouter.Image = b.Images.VersiondRouter
	cfg.Devshardctl.Image = b.Images.Gateway
	cfg.MockDapi.AnnounceVersion = true
}

// recordBuild checks that every image is present locally, so Compose never
// pulls or builds a substitute, and writes build.json with content ids.
func recordBuild(ctx context.Context, build *Build, outputDir string) error {
	record := BuildRecord{Build: *build, ImageIDs: map[string]string{}}
	for _, image := range []string{build.Images.Versiond, build.Images.VersiondRouter, build.Images.Gateway} {
		output, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", image).Output()
		if err != nil {
			return fmt.Errorf("build %s: image %s is not loaded: %w", build.Name, image, err)
		}
		record.ImageIDs[image] = strings.TrimSpace(string(output))
	}
	sum, err := fileSHA256(build.Devshardd)
	if err != nil {
		return fmt.Errorf("build %s: %w", build.Name, err)
	}
	record.DevsharddSHA256 = sum
	if record.DevsharddTags, err = devsharddBuildTags(build.Devshardd); err != nil {
		return fmt.Errorf("build %s: %w", build.Name, err)
	}
	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outputDir, buildFileName), append(body, '\n'), 0o644)
}

// requireTestenvHooks refuses a scenario whose faults live inside devshardd
// when the binary lacks them: such a fault would silently not happen.
func requireTestenvHooks(scenario Scenario, devshardd string) error {
	var uses []string
	for _, event := range scenario.TimelineEvents() {
		if event.Action == tripfileEventKind {
			uses = append(uses, "event "+event.ID)
		}
	}
	for _, host := range scenario.Topology.Hosts {
		for key, value := range host.ResolvedEnv() {
			if strings.HasPrefix(key, testenvEnvPrefix) && value != "" {
				uses = append(uses, host.ID+" "+key)
			}
		}
	}
	if len(uses) == 0 {
		return nil
	}
	tags, err := devsharddBuildTags(devshardd)
	if err != nil {
		return err
	}
	if slices.Contains(strings.Split(tags, ","), testenvBuildTag) {
		return nil
	}
	slices.Sort(uses)
	return fmt.Errorf("%s needs devshardd built with -tags %s, but %s has tags %q", strings.Join(uses, ", "), testenvBuildTag, devshardd, tags)
}

func devsharddBuildTags(path string) (string, error) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read build info of %s: %w", path, err)
	}
	for _, setting := range info.Settings {
		if setting.Key == "-tags" {
			return setting.Value, nil
		}
	}
	return "", nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
