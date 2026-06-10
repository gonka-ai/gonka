package mtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeSelfSignedPair(t *testing.T, dir, name string, dnsNames []string) (certFile, keyFile string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              dnsNames,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	certFile = filepath.Join(dir, name+".crt")
	keyFile = filepath.Join(dir, name+".key")

	certOut, err := os.Create(certFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatal(err)
	}
	certOut.Close()

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyOut, err := os.Create(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		t.Fatal(err)
	}
	keyOut.Close()

	return certFile, keyFile
}

func TestMutualTLSHandshake(t *testing.T) {
	dir := t.TempDir()
	serverCert, serverKey := writeSelfSignedPair(t, dir, "dapi", []string{"localhost"})
	clientCert, clientKey := writeSelfSignedPair(t, dir, "mlnode", []string{"localhost"})

	serverCfg, err := ServerConfig(serverCert, serverKey, clientCert)
	if err != nil {
		t.Fatalf("ServerConfig: %v", err)
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	srv.TLS = serverCfg
	srv.StartTLS()
	defer srv.Close()

	t.Run("pinned client is accepted", func(t *testing.T) {
		clientCfg, err := ClientConfig(clientCert, clientKey, serverCert)
		if err != nil {
			t.Fatalf("ClientConfig: %v", err)
		}
		httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: clientCfg}}

		resp, err := httpClient.Get(srv.URL)
		if err != nil {
			t.Fatalf("request with pinned certs failed: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if string(body) != "ok" {
			t.Fatalf("unexpected body: %q", body)
		}
	})

	t.Run("client without certificate is rejected", func(t *testing.T) {
		serverPool := x509.NewCertPool()
		pemBytes, _ := os.ReadFile(serverCert)
		serverPool.AppendCertsFromPEM(pemBytes)
		httpClient := &http.Client{Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: serverPool},
		}}

		if _, err := httpClient.Get(srv.URL); err == nil {
			t.Fatal("expected handshake failure for client without certificate")
		}
	})

	t.Run("client with foreign certificate is rejected", func(t *testing.T) {
		foreignCert, foreignKey := writeSelfSignedPair(t, dir, "attacker", []string{"localhost"})
		clientCfg, err := ClientConfig(foreignCert, foreignKey, serverCert)
		if err != nil {
			t.Fatalf("ClientConfig: %v", err)
		}
		httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: clientCfg}}

		if _, err := httpClient.Get(srv.URL); err == nil {
			t.Fatal("expected handshake failure for client with non-pinned certificate")
		}
	})
}

func TestConfigErrors(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := writeSelfSignedPair(t, dir, "dapi", []string{"localhost"})

	if _, err := ServerConfig("missing.crt", "missing.key", certFile); err == nil {
		t.Fatal("expected error for missing key pair")
	}
	if _, err := ClientConfig(certFile, keyFile, "missing.crt"); err == nil {
		t.Fatal("expected error for missing peer certificate")
	}

	garbage := filepath.Join(dir, "garbage.crt")
	if err := os.WriteFile(garbage, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ClientConfig(certFile, keyFile, garbage); err == nil {
		t.Fatal("expected error for non-PEM peer certificate")
	}
}
