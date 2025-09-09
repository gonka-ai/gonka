package issuer

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	"log/slog"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns"
	"github.com/go-acme/lego/v4/registration"
	"github.com/gonka/proxy-ssl/internal/config"
)

const (
	acmeAccountKeyFile   = "acme_account.key"
	acmeRegistrationFile = "acme_registration.json"
)

// RealACMEProvider implements CertificateProvider using Let's Encrypt
type RealACMEProvider struct {
	logger *slog.Logger
	config *config.Config
}

// acmeUser implements the lego user interface
type acmeUser struct {
	Email        string
	Registration *registration.Resource
	key          crypto.PrivateKey
}

func (u *acmeUser) GetEmail() string                        { return u.Email }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.Registration }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.key }

// loadOrCreateAccount loads the ACME account from disk or creates a new one and persists it.
func (r *RealACMEProvider) loadOrCreateAccount() (*acmeUser, error) {
	// Ensure data path exists
	if err := os.MkdirAll(r.config.DataPath, 0o700); err != nil {
		return nil, fmt.Errorf("ensure data path: %w", err)
	}

	keyPath := filepath.Join(r.config.DataPath, acmeAccountKeyFile)
	regPath := filepath.Join(r.config.DataPath, acmeRegistrationFile)

	var privKey crypto.PrivateKey

	if b, err := os.ReadFile(keyPath); err == nil {
		// Parse PEM (PKCS1 or PKCS8)
		block, _ := pem.Decode(b)
		if block == nil {
			return nil, fmt.Errorf("invalid ACME account key PEM")
		}
		// Try PKCS1 (RSA) first
		if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
			privKey = k
		} else {
			// Try PKCS8
			anyKey, err2 := x509.ParsePKCS8PrivateKey(block.Bytes)
			if err2 != nil {
				return nil, fmt.Errorf("parse ACME account key: %w / %v", err, err2)
			}
			privKey = anyKey
		}
	} else if os.IsNotExist(err) {
		// Generate and persist a new RSA key (PKCS1 PEM)
		k, gerr := rsa.GenerateKey(rand.Reader, 2048)
		if gerr != nil {
			return nil, fmt.Errorf("generate ACME account key: %w", gerr)
		}
		privKey = k
		pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
		if werr := os.WriteFile(keyPath, pemBytes, 0o600); werr != nil {
			return nil, fmt.Errorf("write ACME account key: %w", werr)
		}
	} else {
		return nil, fmt.Errorf("read ACME account key: %w", err)
	}

	user := &acmeUser{
		Email: r.config.ACMEAccountEmail,
		key:   privKey,
	}

	if b, err := os.ReadFile(regPath); err == nil {
		var reg registration.Resource
		if jerr := json.Unmarshal(b, &reg); jerr == nil {
			user.Registration = &reg
		}
	}

	return user, nil
}

// persistRegistration saves the registration resource to disk.
func (r *RealACMEProvider) persistRegistration(res *registration.Resource) error {
	if res == nil {
		return nil
	}
	if err := os.MkdirAll(r.config.DataPath, 0o700); err != nil {
		return fmt.Errorf("ensure data path: %w", err)
	}
	regPath := filepath.Join(r.config.DataPath, acmeRegistrationFile)
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal registration: %w", err)
	}
	if err := os.WriteFile(regPath, b, 0o600); err != nil {
		return fmt.Errorf("write registration: %w", err)
	}
	return nil
}

func (r *RealACMEProvider) GetName() string {
	return "RealACME"
}

// RealACMEProvider implementation with actual ACME integration
func (r *RealACMEProvider) IssueCertificate(csrBytes []byte, fqdns []string) ([]byte, error) {
	r.logger.Info("Issuing real certificate via ACME")

	// Create ACME client
	client, err := r.createLegoClient()
	if err != nil {
		return nil, fmt.Errorf("failed to create ACME client: %w", err)
	}

	// If a CSR is provided, use the CSR path so the issued cert matches the caller's key
	if len(csrBytes) > 0 {
		csr, err := x509.ParseCertificateRequest(csrBytes)
		if err != nil {
			return nil, fmt.Errorf("invalid CSR bytes: %w", err)
		}
		csrReq := certificate.ObtainForCSRRequest{
			CSR:    csr,
			Bundle: true,
		}
		certs, err := client.Certificate.ObtainForCSR(csrReq)
		if err != nil {
			return nil, fmt.Errorf("failed to obtain certificate for CSR: %w", err)
		}
		// Build full chain: leaf first, then issuer/intermediates if provided
		bundle := make([]byte, 0, len(certs.Certificate)+len(certs.IssuerCertificate)+1)
		bundle = append(bundle, certs.Certificate...)
		if len(certs.IssuerCertificate) > 0 {
			bundle = append(bundle, '\n')
			bundle = append(bundle, certs.IssuerCertificate...)
		}
		return bundle, nil
	}

	// Otherwise, obtain a cert by domains (lego will generate a new keypair internally)
	req := certificate.ObtainRequest{
		Domains: fqdns,
		Bundle:  true,
	}
	certs, err := client.Certificate.Obtain(req)
	if err != nil {
		return nil, fmt.Errorf("failed to obtain certificate: %w", err)
	}
	// Build full chain: leaf first, then issuer/intermediates if provided
	bundle := make([]byte, 0, len(certs.Certificate)+len(certs.IssuerCertificate)+1)
	bundle = append(bundle, certs.Certificate...)
	if len(certs.IssuerCertificate) > 0 {
		bundle = append(bundle, '\n')
		bundle = append(bundle, certs.IssuerCertificate...)
	}
	return bundle, nil
}

// createLegoClient creates a lego client with DNS provider
func (r *RealACMEProvider) createLegoClient() (*lego.Client, error) {
	// Load or create persisted ACME account
	user, err := r.loadOrCreateAccount()
	if err != nil {
		return nil, fmt.Errorf("load/create ACME account: %w", err)
	}

	// Create lego config
	config := lego.NewConfig(user)
	config.CADirURL = r.config.ACMEDirectoryURL
	config.Certificate.KeyType = certcrypto.RSA2048

	// Create client
	client, err := lego.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create lego client: %w", err)
	}

	// Ensure the ACME account is registered (agree to TOS) — only if not already persisted
	if user.Registration == nil {
		reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			return nil, fmt.Errorf("failed to register ACME account: %w", err)
		}
		user.Registration = reg
		if err := r.persistRegistration(reg); err != nil {
			r.logger.Warn("Failed to persist ACME registration; continuing", "error", err)
		}
	}

	// Setup DNS provider
	provider, err := r.setupDNSProvider()
	if err != nil {
		return nil, fmt.Errorf("failed to setup DNS provider: %w", err)
	}

	// Add DNS challenge
	err = client.Challenge.SetDNS01Provider(provider)
	if err != nil {
		return nil, fmt.Errorf("failed to set DNS challenge provider: %w", err)
	}

	return client, nil
}

// setupDNSProvider sets up the DNS provider based on configuration
func (r *RealACMEProvider) setupDNSProvider() (challenge.Provider, error) {
	switch r.config.DNSProvider {
	case "route53":
		return dns.NewDNSChallengeProviderByName("route53")
	case "cloudflare":
		return dns.NewDNSChallengeProviderByName("cloudflare")
	case "gcloud":
		return dns.NewDNSChallengeProviderByName("gcloud")
	case "azure":
		return dns.NewDNSChallengeProviderByName("azure")
	case "digitalocean":
		return dns.NewDNSChallengeProviderByName("digitalocean")
	case "hetzner":
		return dns.NewDNSChallengeProviderByName("hetzner")
	default:
		return nil, fmt.Errorf("unsupported DNS provider: %s", r.config.DNSProvider)
	}
}
