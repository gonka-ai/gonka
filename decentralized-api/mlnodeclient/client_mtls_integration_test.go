package mlnodeclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"decentralized-api/internal/mtls"
)

// writeMTLSPair writes a self-signed cert/key pair usable as both a server and
// a client certificate (the PR pins one self-signed cert against the other).
func writeMTLSPair(t *testing.T, dir, name string) (certFile, keyFile string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	template := x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	certFile = filepath.Join(dir, name+".crt")
	keyFile = filepath.Join(dir, name+".key")
	writePEMBlock(t, certFile, "CERTIFICATE", der)

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	writePEMBlock(t, keyFile, "EC PRIVATE KEY", keyDER)

	return certFile, keyFile
}

func writePEMBlock(t *testing.T, path, blockType string, der []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: blockType, Bytes: der}); err != nil {
		t.Fatal(err)
	}
}

// TestClientFactoryMTLS drives the real DAPI-side client (HttpClientFactory ->
// NewNodeClientWithTLS -> NodeState) against a TLS server configured with
// mtls.ServerConfig. This exercises the actual client construction and transport
// wiring end-to-end, which the unit tests in internal/mtls do not cover.
func TestClientFactoryMTLS(t *testing.T) {
	dir := t.TempDir()
	serverCert, serverKey := writeMTLSPair(t, dir, "mlnode")
	clientCert, clientKey := writeMTLSPair(t, dir, "dapi")

	serverCfg, err := mtls.ServerConfig(serverCert, serverKey, clientCert)
	if err != nil {
		t.Fatalf("ServerConfig: %v", err)
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != nodeStatePath {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(StateResponse{State: MlNodeState_INFERENCE, Version: "v3.0.8"})
	}))
	srv.TLS = serverCfg
	srv.StartTLS()
	defer srv.Close()

	t.Run("factory client with pinned cert succeeds", func(t *testing.T) {
		clientCfg, err := mtls.ClientConfig(clientCert, clientKey, serverCert)
		if err != nil {
			t.Fatalf("ClientConfig: %v", err)
		}
		client := (&HttpClientFactory{TLSConfig: clientCfg}).CreateClient(srv.URL, srv.URL)

		state, err := client.NodeState(context.Background())
		if err != nil {
			t.Fatalf("NodeState over mTLS failed: %v", err)
		}
		if state.State != MlNodeState_INFERENCE {
			t.Fatalf("unexpected state: got %q want %q", state.State, MlNodeState_INFERENCE)
		}
	})

	t.Run("client trusting server but without client cert is rejected", func(t *testing.T) {
		pool := x509.NewCertPool()
		pemBytes, err := os.ReadFile(serverCert)
		if err != nil {
			t.Fatal(err)
		}
		pool.AppendCertsFromPEM(pemBytes)
		clientCfg := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		client := (&HttpClientFactory{TLSConfig: clientCfg}).CreateClient(srv.URL, srv.URL)

		if _, err := client.NodeState(context.Background()); err == nil {
			t.Fatal("expected server to reject client without a client certificate")
		}
	})

	t.Run("factory client with foreign cert is rejected", func(t *testing.T) {
		foreignCert, foreignKey := writeMTLSPair(t, dir, "attacker")
		clientCfg, err := mtls.ClientConfig(foreignCert, foreignKey, serverCert)
		if err != nil {
			t.Fatalf("ClientConfig: %v", err)
		}
		client := (&HttpClientFactory{TLSConfig: clientCfg}).CreateClient(srv.URL, srv.URL)

		if _, err := client.NodeState(context.Background()); err == nil {
			t.Fatal("expected server to reject a non-pinned client certificate")
		}
	})
}
