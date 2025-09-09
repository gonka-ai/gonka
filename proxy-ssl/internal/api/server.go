package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gonka/proxy-ssl/internal/config"
	"github.com/gonka/proxy-ssl/internal/issuer"
)

// Server represents the HTTP API server
type Server struct {
	config *config.Config
	issuer *issuer.Issuer
	logger *slog.Logger
	router *gin.Engine
}

// NewServer creates a new API server
func NewServer(cfg *config.Config, certIssuer *issuer.Issuer, logger *slog.Logger) *Server {
	server := &Server{
		config: cfg,
		issuer: certIssuer,
		logger: logger,
	}

	// Setup router
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(server.loggingMiddleware())

	// Health check
	router.GET("/health", server.healthHandler)

	// API routes
	v1 := router.Group("/v1")
	{
		// Public endpoints (no auth required)
		v1.POST("/tokens", server.generateToken)

		certs := v1.Group("/certs")
		certs.Use(server.authMiddleware())
		{
			// One-call automatic certificate endpoint
			certs.POST("/auto", server.createAutoCertificate)

			// Legacy endpoints for backward compatibility
			certs.POST("/orders", server.createOrder)
			certs.GET("/orders/:id", server.getOrder)
			certs.GET("/orders/:id/bundle", server.getCertificateBundle)
			certs.POST("/orders/:id/renew", server.renewCertificate)
		}
	}

	server.router = router
	return server
}

// Router returns the HTTP router
func (s *Server) Router() *gin.Engine {
	return s.router
}

// healthHandler handles health check requests
func (s *Server) healthHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "healthy",
		"time":   time.Now().UTC(),
	})
}

// createAutoCertificate handles automatic certificate creation (one-call solution)
func (s *Server) createAutoCertificate(c *gin.Context) {
	var req AutoCertificateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		s.logger.Error("Invalid request body", "error", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	// Get node ID from JWT
	nodeID := c.GetString("node_id")

	// Validate request
	if err := s.validateAutoCertificateRequest(&req, nodeID); err != nil {
		s.logger.Error("Invalid auto certificate request", "error", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Generate private key
	privateKey, err := s.generatePrivateKey()
	if err != nil {
		s.logger.Error("Failed to generate private key", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate private key"})
		return
	}

	// Generate CSR
	csrBytes, err := s.generateCSR(privateKey, req.FQDNs)
	if err != nil {
		s.logger.Error("Failed to generate CSR", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate CSR"})
		return
	}

	// Submit to proxy-ssl
	order, err := s.issuer.CreateOrder(c.Request.Context(), nodeID, base64.StdEncoding.EncodeToString(csrBytes), req.FQDNs)
	if err != nil {
		s.logger.Error("Failed to create order", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create order"})
		return
	}

	// Persist the private key alongside the certificate bundle for renewals
	if err := os.MkdirAll(s.config.CertStoragePath, 0o700); err == nil {
		if werr := os.WriteFile(filepath.Join(s.config.CertStoragePath, order.ID+".key"), privateKey, 0o600); werr != nil {
			s.logger.Warn("Failed to persist private key for renewal", "error", werr)
		}
	} else {
		s.logger.Warn("Failed to ensure cert storage path for key persistence", "error", err)
	}

	// Wait for certificate to be issued
	certBundle, err := s.waitForCertificate(nodeID, order.ID)
	if err != nil {
		s.logger.Error("Failed to get certificate", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get certificate"})
		return
	}

	// Return complete certificate response
	response := CertificateResponse{
		OrderID:     order.ID,
		NodeID:      nodeID,
		PrivateKey:  string(privateKey),
		Certificate: string(certBundle),
		Status:      "completed",
		ExpiresAt:   order.ExpiresAt.Format(time.RFC3339),
	}

	c.JSON(http.StatusCreated, response)
}

// createOrder handles certificate order creation (legacy)
func (s *Server) createOrder(c *gin.Context) {
	var req CreateOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		s.logger.Error("Invalid request body", "error", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	// Validate request
	if err := s.validateCreateOrderRequest(&req); err != nil {
		s.logger.Error("Invalid create order request", "error", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Create order (this would need to be updated to handle CSR generation)
	c.JSON(http.StatusNotImplemented, gin.H{"error": "Use /auto endpoint instead"})
}

// getOrder handles order status requests
func (s *Server) getOrder(c *gin.Context) {
	orderID := c.Param("id")
	if orderID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Order ID is required"})
		return
	}

	// Get node ID from JWT
	nodeID := c.GetString("node_id")

	// Get order
	order, err := s.issuer.GetOrder(c.Request.Context(), nodeID, orderID)
	if err != nil {
		s.logger.Error("Failed to get order", "error", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Order not found"})
		return
	}

	c.JSON(http.StatusOK, order)
}

// getCertificateBundle handles certificate bundle download
func (s *Server) getCertificateBundle(c *gin.Context) {
	orderID := c.Param("id")
	if orderID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Order ID is required"})
		return
	}

	// Get node ID from JWT
	nodeID := c.GetString("node_id")

	// Get certificate bundle
	bundle, err := s.issuer.GetCertificateBundle(c.Request.Context(), nodeID, orderID)
	if err != nil {
		s.logger.Error("Failed to get certificate bundle", "error", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Certificate bundle not found"})
		return
	}

	c.Data(http.StatusOK, "application/x-pem-file", bundle)
}

// renewCertificate handles certificate renewal requests
func (s *Server) renewCertificate(c *gin.Context) {
	orderID := c.Param("id")
	if orderID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Order ID is required"})
		return
	}

	// Get node ID from JWT
	nodeID := c.GetString("node_id")

	// Renew certificate
	err := s.issuer.RenewCertificate(c.Request.Context(), nodeID, orderID)
	if err != nil {
		s.logger.Error("Failed to renew certificate", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to renew certificate"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Certificate renewal initiated"})
}

// generateToken handles JWT token generation
func (s *Server) generateToken(c *gin.Context) {
	var req GenerateTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		s.logger.Error("Invalid request body", "error", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	// Validate request
	if err := s.validateGenerateTokenRequest(&req); err != nil {
		s.logger.Error("Invalid token generation request", "error", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Generate JWT token
	token, err := s.createJWTToken(req.NodeID, req.ExpiresInDays)
	if err != nil {
		s.logger.Error("Failed to generate token", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate token"})
		return
	}

	response := TokenResponse{
		NodeID:        req.NodeID,
		Token:         token,
		ExpiresInDays: req.ExpiresInDays,
		ExpiresAt:     time.Now().AddDate(0, 0, req.ExpiresInDays).Format(time.RFC3339),
	}

	c.JSON(http.StatusCreated, response)
}

// validateAutoCertificateRequest validates the auto certificate request
func (s *Server) validateAutoCertificateRequest(req *AutoCertificateRequest, nodeID string) error {
	if req.NodeID != nodeID {
		return fmt.Errorf("node_id mismatch")
	}

	if len(req.FQDNs) == 0 {
		return fmt.Errorf("at least one FQDN is required")
	}

	// Validate FQDNs
	for _, fqdn := range req.FQDNs {
		if !s.isAllowedFQDN(fqdn) {
			return fmt.Errorf("FQDN %s is not allowed", fqdn)
		}
	}

	return nil
}

// validateCreateOrderRequest validates the create order request (legacy)
func (s *Server) validateCreateOrderRequest(req *CreateOrderRequest) error {
	if len(req.FQDNs) == 0 {
		return fmt.Errorf("at least one FQDN is required")
	}

	// Validate FQDNs
	for _, fqdn := range req.FQDNs {
		if !s.isAllowedFQDN(fqdn) {
			return fmt.Errorf("FQDN %s is not allowed", fqdn)
		}
	}

	return nil
}

// validateGenerateTokenRequest validates the token generation request
func (s *Server) validateGenerateTokenRequest(req *GenerateTokenRequest) error {
	if req.NodeID == "" {
		return fmt.Errorf("node_id is required")
	}

	if req.ExpiresInDays <= 0 {
		return fmt.Errorf("expires_in_days must be positive")
	}

	if req.ExpiresInDays > 365 {
		return fmt.Errorf("expires_in_days cannot exceed 365 days")
	}

	return nil
}

// createJWTToken creates a JWT token for the given node ID
func (s *Server) createJWTToken(nodeID string, expiresInDays int) (string, error) {
	// Token payload
	payload := jwt.MapClaims{
		"node_id": nodeID,
		"iat":     time.Now().Unix(),
		"exp":     time.Now().AddDate(0, 0, expiresInDays).Unix(),
	}

	// Generate token
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, payload)
	tokenString, err := token.SignedString([]byte(s.config.JWTSecret))
	if err != nil {
		return "", fmt.Errorf("failed to sign token: %w", err)
	}

	return tokenString, nil
}

// isAllowedFQDN checks if the FQDN is allowed based on configuration
func (s *Server) isAllowedFQDN(fqdn string) bool {
	// Check if FQDN ends with configured domain
	if !strings.HasSuffix(fqdn, "."+s.config.Domain) && fqdn != s.config.Domain {
		return false
	}

	// If no subdomain restrictions, allow all
	if len(s.config.AllowedSubdomains) == 0 {
		return true
	}

	// Check if subdomain is allowed
	parts := strings.Split(fqdn, ".")
	if len(parts) < 2 {
		return false
	}

	subdomain := parts[0]
	for _, allowed := range s.config.AllowedSubdomains {
		if allowed == subdomain {
			return true
		}
	}

	return false
}

// generatePrivateKey generates a new RSA private key
func (s *Server) generatePrivateKey() ([]byte, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("failed to generate private key: %w", err)
	}

	// Encode to PEM
	privateKeyPEM := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	}

	return pem.EncodeToMemory(privateKeyPEM), nil
}

// generateCSR generates a CSR for the given private key and FQDNs
func (s *Server) generateCSR(privateKeyPEM []byte, fqdns []string) ([]byte, error) {
	// Decode private key
	block, _ := pem.Decode(privateKeyPEM)
	if block == nil {
		return nil, fmt.Errorf("failed to decode private key")
	}

	privateKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key: %w", err)
	}

	// Create CSR template
	csrTemplate := &x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName: fqdns[0], // Use first FQDN as CN
		},
		DNSNames: fqdns,
	}

	// Create CSR
	csrBytes, err := x509.CreateCertificateRequest(rand.Reader, csrTemplate, privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create CSR: %w", err)
	}

	return csrBytes, nil
}

// waitForCertificate waits for a certificate to be issued
func (s *Server) waitForCertificate(nodeID, orderID string) ([]byte, error) {
	maxAttempts := 30
	attempt := 1

	for attempt <= maxAttempts {
		order, err := s.issuer.GetOrder(context.Background(), nodeID, orderID)
		if err != nil {
			return nil, err
		}

		switch order.Status {
		case "completed":
			return s.issuer.GetCertificateBundle(context.Background(), nodeID, orderID)
		case "failed":
			return nil, fmt.Errorf("certificate issuance failed")
		case "processing", "pending":
			s.logger.Debug("Certificate still being processed...", "order_id", orderID, "attempt", attempt, "max_attempts", maxAttempts)
			time.Sleep(10 * time.Second)
		default:
			s.logger.Warn("Unknown order status", "order_id", orderID, "status", order.Status)
			time.Sleep(10 * time.Second)
		}

		attempt++
	}

	return nil, fmt.Errorf("timeout waiting for certificate issuance")
}

// authMiddleware handles JWT authentication
func (s *Server) authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization header required"})
			c.Abort()
			return
		}

		// Extract token from Bearer header
		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid authorization header format"})
			c.Abort()
			return
		}

		tokenString := parts[1]

		// Parse and validate JWT
		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return []byte(s.config.JWTSecret), nil
		})

		if err != nil || !token.Valid {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			c.Abort()
			return
		}

		// Extract claims
		if claims, ok := token.Claims.(jwt.MapClaims); ok {
			if nodeID, ok := claims["node_id"].(string); ok {
				c.Set("node_id", nodeID)
			} else {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token claims"})
				c.Abort()
				return
			}
		} else {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token claims"})
			c.Abort()
			return
		}

		c.Next()
	}
}

// loggingMiddleware adds request logging
func (s *Server) loggingMiddleware() gin.HandlerFunc {
	return gin.LoggerWithFormatter(func(param gin.LogFormatterParams) string {
		s.logger.Info("HTTP request",
			"method", param.Method,
			"path", param.Path,
			"status", param.StatusCode,
			"latency", param.Latency,
			"client_ip", param.ClientIP,
			"user_agent", param.Request.UserAgent(),
		)
		return ""
	})
}

// CreateOrderRequest represents a certificate order creation request (legacy)
type CreateOrderRequest struct {
	CSR   string   `json:"csr" binding:"required"`
	FQDNs []string `json:"fqdns" binding:"required"`
}

// AutoCertificateRequest represents an automatic certificate request
type AutoCertificateRequest struct {
	NodeID string   `json:"node_id" binding:"required"`
	FQDNs  []string `json:"fqdns" binding:"required"`
}

// CertificateResponse represents a complete certificate response
type CertificateResponse struct {
	OrderID     string `json:"order_id"`
	NodeID      string `json:"node_id"`
	PrivateKey  string `json:"private_key"`
	Certificate string `json:"certificate"`
	Status      string `json:"status"`
	ExpiresAt   string `json:"expires_at,omitempty"`
}

// GenerateTokenRequest represents a request to generate a JWT token
type GenerateTokenRequest struct {
	NodeID        string `json:"node_id" binding:"required"`
	ExpiresInDays int    `json:"expires_in_days" binding:"required"`
}

// TokenResponse represents a JWT token response
type TokenResponse struct {
	NodeID        string `json:"node_id"`
	Token         string `json:"token"`
	ExpiresInDays int    `json:"expires_in_days"`
	ExpiresAt     string `json:"expires_at"`
}
