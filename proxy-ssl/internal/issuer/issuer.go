package issuer

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"log/slog"

	"github.com/gonka/proxy-ssl/internal/config"
)

// CertificateProvider defines the interface for certificate issuance
type CertificateProvider interface {
	IssueCertificate(csrBytes []byte, fqdns []string) ([]byte, error)
	GetName() string
}

// Order represents a certificate order
type Order struct {
	ID        string    `json:"id"`
	NodeID    string    `json:"node_id"`
	FQDNs     []string  `json:"fqdns"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Issuer handles certificate issuance using ACME DNS-01
type Issuer struct {
	config   *config.Config
	logger   *slog.Logger
	provider CertificateProvider
	orders   map[string]*Order
	mu       sync.RWMutex
}

// New creates a new certificate issuer
func New(cfg *config.Config, logger *slog.Logger) (*Issuer, error) {
	var provider CertificateProvider

	// Always use real ACME provider
	provider = &RealACMEProvider{logger: logger, config: cfg}
	logger.Info("Using real ACME certificate provider")

	issuer := &Issuer{
		config:   cfg,
		logger:   logger,
		provider: provider,
		orders:   make(map[string]*Order),
	}

	return issuer, nil
}

// CreateOrder creates a new certificate order
func (i *Issuer) CreateOrder(ctx context.Context, nodeID, csrBase64 string, fqdns []string) (*Order, error) {
	// Decode CSR
	csrBytes, err := base64.StdEncoding.DecodeString(csrBase64)
	if err != nil {
		return nil, fmt.Errorf("invalid CSR encoding: %w", err)
	}

	// Parse CSR
	csr, err := x509.ParseCertificateRequest(csrBytes)
	if err != nil {
		return nil, fmt.Errorf("invalid CSR: %w", err)
	}

	// Validate CSR matches FQDNs
	if err := i.validateCSR(csr, fqdns); err != nil {
		return nil, fmt.Errorf("CSR validation failed: %w", err)
	}

	// Create order
	order := &Order{
		ID:        generateOrderID(),
		NodeID:    nodeID,
		FQDNs:     fqdns,
		Status:    "pending",
		CreatedAt: time.Now(),
	}

	// Store order
	i.mu.Lock()
	i.orders[order.ID] = order
	i.mu.Unlock()

	// Start certificate issuance in background
	go i.issueCertificate(order.ID, csrBytes, fqdns)

	return order, nil
}

// GetOrder retrieves an order by ID
func (i *Issuer) GetOrder(ctx context.Context, nodeID, orderID string) (*Order, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()

	order, exists := i.orders[orderID]
	if !exists {
		return nil, fmt.Errorf("order not found")
	}

	if order.NodeID != nodeID {
		return nil, fmt.Errorf("access denied")
	}

	return order, nil
}

// GetCertificateBundle retrieves the certificate bundle for an order
func (i *Issuer) GetCertificateBundle(ctx context.Context, nodeID, orderID string) ([]byte, error) {
	// Verify order exists and belongs to node
	order, err := i.GetOrder(ctx, nodeID, orderID)
	if err != nil {
		return nil, err
	}

	if order.Status != "completed" {
		return nil, fmt.Errorf("order not completed")
	}

	// Read certificate bundle from file
	bundlePath := filepath.Join(i.config.CertStoragePath, orderID+".pem")
	bundle, err := os.ReadFile(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read certificate bundle: %w", err)
	}

	return bundle, nil
}

// RenewCertificate renews a certificate
func (i *Issuer) RenewCertificate(ctx context.Context, nodeID, orderID string) error {
	// Verify order exists and belongs to node
	order, err := i.GetOrder(ctx, nodeID, orderID)
	if err != nil {
		return err
	}

	if order.Status != "completed" {
		return fmt.Errorf("order not completed")
	}

	// Check if renewal is needed (cert expires in 30 days)
	if time.Until(order.ExpiresAt) > 30*24*time.Hour {
		return fmt.Errorf("certificate does not need renewal yet")
	}

	// Start renewal in background
	go i.renewCertificate(orderID)

	return nil
}

// issueCertificate issues a certificate using the configured provider
func (i *Issuer) issueCertificate(orderID string, csrBytes []byte, fqdns []string) {
	// Update order status
	i.mu.Lock()
	order := i.orders[orderID]
	order.Status = "processing"
	i.mu.Unlock()

	i.logger.Info("Issuing certificate", "order_id", orderID, "provider", i.provider.GetName())

	// Issue certificate using the configured provider
	certBundle, err := i.provider.IssueCertificate(csrBytes, fqdns)
	if err != nil {
		i.logger.Error("Failed to issue certificate", "order_id", orderID, "error", err)

		i.mu.Lock()
		order.Status = "failed"
		i.mu.Unlock()
		return
	}

	// Save certificate bundle
	bundlePath := filepath.Join(i.config.CertStoragePath, orderID+".pem")
	err = os.WriteFile(bundlePath, certBundle, 0644)
	if err != nil {
		i.logger.Error("Failed to save certificate bundle", "order_id", orderID, "error", err)

		i.mu.Lock()
		order.Status = "failed"
		i.mu.Unlock()
		return
	}

	// Parse PEM bundle and extract leaf certificate expiry
	var leaf *x509.Certificate
	rest := certBundle
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type == "CERTIFICATE" {
			c, err := x509.ParseCertificate(block.Bytes)
			if err == nil && leaf == nil {
				leaf = c
			}
		}
	}

	if leaf == nil {
		i.logger.Error("No certificate found in PEM bundle", "order_id", orderID)
		i.mu.Lock()
		order.ExpiresAt = time.Now().AddDate(0, 0, 90)
		i.mu.Unlock()
	} else {
		i.mu.Lock()
		order.ExpiresAt = leaf.NotAfter
		i.mu.Unlock()
	}

	// Update order status
	i.mu.Lock()
	order.Status = "completed"
	i.mu.Unlock()

	i.logger.Info("Certificate issued successfully", "order_id", orderID)
}

// renewCertificate renews an existing certificate
func (i *Issuer) renewCertificate(orderID string) {
	// Get order
	i.mu.RLock()
	order, exists := i.orders[orderID]
	i.mu.RUnlock()

	if !exists {
		i.logger.Error("Order not found for renewal", "order_id", orderID)
		return
	}

	// Update order status
	i.mu.Lock()
	order.Status = "renewing"
	i.mu.Unlock()

	// Load stored private key
	keyPath := filepath.Join(i.config.CertStoragePath, orderID+".key")
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		i.logger.Error("Failed to read stored private key for renewal", "order_id", orderID, "error", err)
		return
	}

	// Generate CSR from stored key
	csrBytes, err := generateCSRFromKeyPEM(keyPEM, order.FQDNs)
	if err != nil {
		i.logger.Error("Failed to generate CSR for renewal", "order_id", orderID, "error", err)
		return
	}

	// Issue new certificate using the same key
	certBundle, err := i.provider.IssueCertificate(csrBytes, order.FQDNs)
	if err != nil {
		i.logger.Error("Failed to renew certificate", "order_id", orderID, "error", err)
		return
	}

	// Save renewed certificate
	bundlePath := filepath.Join(i.config.CertStoragePath, orderID+".pem")
	if err := os.WriteFile(bundlePath, certBundle, 0644); err != nil {
		i.logger.Error("Failed to save renewed certificate", "order_id", orderID, "error", err)
		return
	}

	// Update expiry from renewed bundle
	var leaf *x509.Certificate
	rest := certBundle
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type == "CERTIFICATE" {
			c, err := x509.ParseCertificate(block.Bytes)
			if err == nil && leaf == nil {
				leaf = c
			}
		}
	}
	i.mu.Lock()
	if leaf != nil {
		order.ExpiresAt = leaf.NotAfter
	} else {
		order.ExpiresAt = time.Now().AddDate(0, 0, 90)
	}
	order.Status = "completed"
	i.mu.Unlock()

	i.logger.Info("Certificate renewal completed", "order_id", orderID)
}

// validateCSR validates that the CSR matches the requested FQDNs
func (i *Issuer) validateCSR(csr *x509.CertificateRequest, fqdns []string) error {
	// Check if CSR contains all requested FQDNs
	for _, fqdn := range fqdns {
		found := false
		for _, dnsName := range csr.DNSNames {
			if dnsName == fqdn {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("CSR does not contain FQDN: %s", fqdn)
		}
	}

	return nil
}

// generateOrderID generates a unique order ID
func generateOrderID() string {
	return fmt.Sprintf("order_%d", time.Now().UnixNano())
}

// generateCSRFromKeyPEM generates a CSR using the provided private key PEM and FQDNs.
func generateCSRFromKeyPEM(privateKeyPEM []byte, fqdns []string) ([]byte, error) {
	block, _ := pem.Decode(privateKeyPEM)
	if block == nil {
		return nil, fmt.Errorf("failed to decode private key PEM")
	}

	// Support RSA PKCS1 keys
	rsaKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		// Try PKCS8
		keyAny, err2 := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err2 != nil {
			return nil, fmt.Errorf("failed to parse private key: %w / %v", err, err2)
		}
		var ok bool
		rsaKey, ok = keyAny.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("unsupported private key type in PKCS8: %T", keyAny)
		}
	}

	tmpl := &x509.CertificateRequest{Subject: pkix.Name{CommonName: fqdns[0]}, DNSNames: fqdns}
	return x509.CreateCertificateRequest(rand.Reader, tmpl, rsaKey)
}
