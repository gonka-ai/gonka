package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"log/slog"

	"github.com/spf13/viper"
)

// Config holds all configuration for the proxy-ssl service
type Config struct {
	// Server configuration
	Port     int
	LogLevel slog.Level

	// ACME configuration
	ACMEDirectoryURL string
	ACMEAccountEmail string

	// DNS provider configuration
	DNSProvider       string
	DNSProviderConfig map[string]string

	// Domain configuration
	Domain            string
	AllowedSubdomains []string

	// Security configuration
	JWTSecret string

	// Storage configuration
	CertStoragePath string
	DataPath        string
}

// Load loads configuration from environment variables and files
func Load() (*Config, error) {
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	// Set defaults

	// if a “safe” environment is ever need , point acme_directory_url at
	// Let’s Encrypt Staging (recommended by LE) to avoid production
	// rate limits during testing:
	// https://acme-staging-v02.api.letsencrypt.org/directory.

	viper.SetDefault("port", 8080)
	viper.SetDefault("log_level", "info")
	// Default ACME directory: production, unless ACME_ENV=staging and no explicit override provided
	acmeDefault := "https://acme-v02.api.letsencrypt.org/directory"
	if strings.EqualFold(os.Getenv("ACME_ENV"), "staging") {
		acmeDefault = "https://acme-staging-v02.api.letsencrypt.org/directory"
	}
	viper.SetDefault("acme_directory_url", acmeDefault)
	viper.SetDefault("cert_storage_path", "/app/certs")
	viper.SetDefault("data_path", "/app/data")

	// Load configuration
	cfg := &Config{
		Port:              viper.GetInt("port"),
		LogLevel:          getLogLevel(viper.GetString("log_level")),
		ACMEDirectoryURL:  viper.GetString("acme_directory_url"),
		ACMEAccountEmail:  viper.GetString("acme_account_email"),
		DNSProvider:       viper.GetString("acme_dns_provider"),
		Domain:            viper.GetString("cert_issuer_domain"),
		AllowedSubdomains: filterEmpty(strings.Split(strings.TrimSpace(viper.GetString("cert_issuer_allowed_subdomains")), ",")),
		JWTSecret:         viper.GetString("cert_issuer_jwt_secret"),
		CertStoragePath:   viper.GetString("cert_storage_path"),
		DataPath:          viper.GetString("data_path"),
	}

	// Load DNS provider configuration from environment
	cfg.DNSProviderConfig = loadDNSProviderConfig()

	// Special handling for Google Cloud DNS: decode service account JSON from base64 if provided
	if cfg.DNSProvider == "gcloud" {
		b64 := cfg.DNSProviderConfig["GCE_SERVICE_ACCOUNT_JSON_B64"]
		if b64 != "" {
			// Decode and write to a file, set env var for lego
			decoded, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				return nil, fmt.Errorf("failed to decode GCE_SERVICE_ACCOUNT_JSON_B64: %w", err)
			}
			tmpFile := "/tmp/gce_service_account.json"
			if err := os.WriteFile(tmpFile, decoded, 0600); err != nil {
				return nil, fmt.Errorf("failed to write GCE service account file: %w", err)
			}
			cfg.DNSProviderConfig["GCE_SERVICE_ACCOUNT_FILE"] = tmpFile
			os.Setenv("GCE_SERVICE_ACCOUNT_FILE", tmpFile)
		}
	}

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return cfg, nil
}

// Validate validates the configuration
func (c *Config) Validate() error {
	if c.ACMEAccountEmail == "" {
		return fmt.Errorf("ACME account email is required")
	}

	if c.DNSProvider == "" {
		return fmt.Errorf("DNS provider is required")
	}

	if c.Domain == "" {
		return fmt.Errorf("domain is required")
	}

	if c.JWTSecret == "" {
		return fmt.Errorf("JWT secret is required")
	}

	// Validate DNS provider configuration
	if err := c.validateDNSProviderConfig(); err != nil {
		return fmt.Errorf("DNS provider configuration: %w", err)
	}

	return nil
}

// validateDNSProviderConfig validates DNS provider specific configuration
func (c *Config) validateDNSProviderConfig() error {
	switch c.DNSProvider {
	case "route53":
		if c.DNSProviderConfig["AWS_ACCESS_KEY_ID"] == "" {
			return fmt.Errorf("AWS_ACCESS_KEY_ID is required for Route53")
		}
		if c.DNSProviderConfig["AWS_SECRET_ACCESS_KEY"] == "" {
			return fmt.Errorf("AWS_SECRET_ACCESS_KEY is required for Route53")
		}
	case "cloudflare":
		cfDNS := c.DNSProviderConfig["CF_DNS_API_TOKEN"]
		if cfDNS == "" {
			return fmt.Errorf("Cloudflare credentials required: set CF_DNS_API_TOKEN")
		}
	case "gcloud":
		if c.DNSProviderConfig["GCE_PROJECT"] == "" {
			return fmt.Errorf("GCE_PROJECT is required for Google Cloud DNS")
		}
		if c.DNSProviderConfig["GCE_SERVICE_ACCOUNT_JSON_B64"] == "" {
			return fmt.Errorf("GCE_SERVICE_ACCOUNT_JSON_B64 is required for Google Cloud DNS")
		}
	case "azure":
		if c.DNSProviderConfig["AZURE_CLIENT_ID"] == "" {
			return fmt.Errorf("AZURE_CLIENT_ID is required for Azure DNS")
		}
		if c.DNSProviderConfig["AZURE_CLIENT_SECRET"] == "" {
			return fmt.Errorf("AZURE_CLIENT_SECRET is required for Azure DNS")
		}
		if c.DNSProviderConfig["AZURE_SUBSCRIPTION_ID"] == "" {
			return fmt.Errorf("AZURE_SUBSCRIPTION_ID is required for Azure DNS")
		}
		if c.DNSProviderConfig["AZURE_TENANT_ID"] == "" {
			return fmt.Errorf("AZURE_TENANT_ID is required for Azure DNS")
		}
	case "digitalocean":
		if c.DNSProviderConfig["DO_AUTH_TOKEN"] == "" {
			return fmt.Errorf("DO_AUTH_TOKEN is required for DigitalOcean")
		}
	case "hetzner":
		if c.DNSProviderConfig["HETZNER_API_KEY"] == "" {
			return fmt.Errorf("HETZNER_API_KEY is required for Hetzner")
		}
	default:
		return fmt.Errorf("unsupported DNS provider: %s", c.DNSProvider)
	}

	return nil
}

// loadDNSProviderConfig loads DNS provider specific configuration from environment
func loadDNSProviderConfig() map[string]string {
	config := make(map[string]string)

	// Route53
	config["AWS_ACCESS_KEY_ID"] = os.Getenv("AWS_ACCESS_KEY_ID")
	config["AWS_SECRET_ACCESS_KEY"] = os.Getenv("AWS_SECRET_ACCESS_KEY")
	config["AWS_REGION"] = os.Getenv("AWS_REGION")

	// Cloudflare
	config["CF_DNS_API_TOKEN"] = os.Getenv("CF_DNS_API_TOKEN")

	// Google Cloud DNS
	config["GCE_PROJECT"] = os.Getenv("GCE_PROJECT")
	config["GCE_SERVICE_ACCOUNT_JSON_B64"] = os.Getenv("GCE_SERVICE_ACCOUNT_JSON_B64")

	// Azure DNS
	config["AZURE_CLIENT_ID"] = os.Getenv("AZURE_CLIENT_ID")
	config["AZURE_CLIENT_SECRET"] = os.Getenv("AZURE_CLIENT_SECRET")
	config["AZURE_SUBSCRIPTION_ID"] = os.Getenv("AZURE_SUBSCRIPTION_ID")
	config["AZURE_TENANT_ID"] = os.Getenv("AZURE_TENANT_ID")

	// DigitalOcean
	config["DO_AUTH_TOKEN"] = os.Getenv("DO_AUTH_TOKEN")

	// Hetzner
	config["HETZNER_API_KEY"] = os.Getenv("HETZNER_API_KEY")

	return config
}

// getLogLevel converts string log level to slog.Level
func getLogLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	case "fatal":
		return slog.LevelError
	case "panic":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// filterEmpty trims and removes empty elements from a slice of strings.
func filterEmpty(values []string) []string {
	var out []string
	for _, v := range values {
		t := strings.TrimSpace(v)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}
