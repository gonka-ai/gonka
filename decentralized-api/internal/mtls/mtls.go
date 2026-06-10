package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

func ServerConfig(certFile, keyFile, peerCertFile string) (*tls.Config, error) {
	cert, peerPool, err := loadCertAndPeerPool(certFile, keyFile, peerCertFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		ClientCAs:    peerPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}, nil
}

func ClientConfig(certFile, keyFile, peerCertFile string) (*tls.Config, error) {
	cert, peerPool, err := loadCertAndPeerPool(certFile, keyFile, peerCertFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		RootCAs:      peerPool,
	}, nil
}

func loadCertAndPeerPool(certFile, keyFile, peerCertFile string) (tls.Certificate, *x509.CertPool, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("mtls: loading key pair (%s, %s): %w", certFile, keyFile, err)
	}

	peerPEM, err := os.ReadFile(peerCertFile)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("mtls: reading peer certificate %s: %w", peerCertFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(peerPEM) {
		return tls.Certificate{}, nil, fmt.Errorf("mtls: no valid PEM certificates in %s", peerCertFile)
	}
	return cert, pool, nil
}
