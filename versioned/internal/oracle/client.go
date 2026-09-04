package oracle

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

type VersionConfig struct {
	Versions []Version `json:"versions"`
}

type Version struct {
	Name   string `json:"name"`
	Binary string `json:"binary"`
	SHA256 string `json:"sha256,omitempty"`
}

// ResolvedSHA256 returns the sha256 checksum for this version.
// Priority: sha256 field, then ?checksum=sha256:... in URL query.
func (v Version) ResolvedSHA256() (string, error) {
	if v.SHA256 != "" {
		return validateSHA256(v.Name, v.SHA256)
	}
	u, err := url.Parse(v.Binary)
	if err != nil {
		return "", fmt.Errorf("parse binary URL: %w", err)
	}
	cs := u.Query().Get("checksum")
	if after, ok := strings.CutPrefix(cs, "sha256:"); ok {
		hash := after
		if hash == "" {
			return "", fmt.Errorf("empty sha256 checksum in URL for version %s", v.Name)
		}
		return validateSHA256(v.Name, hash)
	}
	return "", fmt.Errorf("no checksum for version %s: sha256 field empty and no ?checksum=sha256: in URL", v.Name)
}

type Client struct {
	url        string
	httpClient *http.Client
}

func NewClient(oracleURL string) *Client {
	return &Client{
		url: oracleURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *Client) Fetch(ctx context.Context) (VersionConfig, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return VersionConfig{}, fmt.Errorf("create request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return VersionConfig{}, fmt.Errorf("fetch versions: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return VersionConfig{}, fmt.Errorf("oracle returned status %d", resp.StatusCode)
	}

	var cfg VersionConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return VersionConfig{}, fmt.Errorf("decode response: %w", err)
	}
	if err := validateVersions(cfg.Versions); err != nil {
		return VersionConfig{}, err
	}
	return cfg, nil
}

func validateVersions(versions []Version) error {
	seen := make(map[string]struct{}, len(versions))
	for i, v := range versions {
		if !validVersionName(v.Name) {
			return fmt.Errorf("invalid oracle version name at index %d: %q", i, v.Name)
		}
		if _, dup := seen[v.Name]; dup {
			return fmt.Errorf("duplicate oracle version name %q", v.Name)
		}
		if _, err := v.ResolvedSHA256(); err != nil {
			return fmt.Errorf("invalid oracle version %q sha256: %w", v.Name, err)
		}
		seen[v.Name] = struct{}{}
	}
	return nil
}

func validateSHA256(versionName, hash string) (string, error) {
	if len(hash) != 64 {
		return "", fmt.Errorf("sha256 for version %s must be 64 hex characters, got %d", versionName, len(hash))
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return "", fmt.Errorf("sha256 for version %s is not valid hex: %w", versionName, err)
	}
	return hash, nil
}

func validVersionName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.TrimSpace(name) != name {
		return false
	}
	if filepath.IsAbs(name) || strings.ContainsAny(name, `/\`) {
		return false
	}
	return filepath.Base(name) == name
}
