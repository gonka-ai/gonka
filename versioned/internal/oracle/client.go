package oracle

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
		return v.SHA256, nil
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
		return hash, nil
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
	return cfg, nil
}
