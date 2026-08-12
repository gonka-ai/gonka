package oracle

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type VersionConfig struct {
	Schema      int       `json:"schema"`
	Initialized bool      `json:"initialized"`
	Revision    int64     `json:"revision"`
	Versions    []Version `json:"versions"`
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
	if strings.HasPrefix(cs, "sha256:") {
		hash := strings.TrimPrefix(cs, "sha256:")
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
	verifier   CatalogVerifier
}

type ClientOption func(*Client)

// CatalogVerifier independently authenticates a catalog before it can become
// versiond's durable desired state.
type CatalogVerifier interface {
	Verify(context.Context, VersionConfig) error
}

func WithCatalogVerifier(verifier CatalogVerifier) ClientOption {
	return func(client *Client) {
		client.verifier = verifier
	}
}

func NewClient(oracleURL string, options ...ClientOption) *Client {
	client := &Client{
		url: oracleURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
	for _, option := range options {
		option(client)
	}
	return client
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
	if err := validateCatalog(cfg); err != nil {
		return VersionConfig{}, err
	}
	if c.verifier != nil {
		if err := c.verifier.Verify(ctx, cfg); err != nil {
			return VersionConfig{}, fmt.Errorf("verify oracle catalog against consensus: %w", err)
		}
	}
	return cfg, nil
}

func validateCatalog(cfg VersionConfig) error {
	if cfg.Schema != 1 {
		return fmt.Errorf("unsupported oracle catalog schema %d", cfg.Schema)
	}
	if !cfg.Initialized {
		return fmt.Errorf("oracle catalog is not initialized")
	}
	if cfg.Revision < 0 {
		return fmt.Errorf("oracle catalog revision must be non-negative, got %d", cfg.Revision)
	}
	return validateVersions(cfg.Versions)
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
	if len(name) == 0 || len(name) > 64 || !asciiAlphaNumeric(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !asciiAlphaNumeric(name[i]) && !strings.ContainsRune("._+~-", rune(name[i])) {
			return false
		}
	}
	return true
}

func asciiAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9'
}
