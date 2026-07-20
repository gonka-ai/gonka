package e2e

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/e2e/testutil"
	"devshard/signing"
)

const (
	defaultEscrowID = "1"

	mockChainAlias  = "mock-chain"
	devshardCtlName = "devshardctl"
	postgresAlias   = "postgres"
)

type e2eImages struct {
	mockChain   string
	host        string
	devshardctl string
	postgres    string
}

func signerAddress(t *testing.T, privateKey string) string {
	t.Helper()
	signer, err := signing.SignerFromHex(privateKey)
	require.NoError(t, err)
	return signer.Address()
}

func requireE2EEnabled(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping devshard e2e in -short mode")
	}
	if os.Getenv("DEVSHARD_E2E") != "1" {
		t.Skip("set DEVSHARD_E2E=1 to run Docker-backed devshard e2e tests")
	}
}

func requiredImages(t *testing.T) e2eImages {
	t.Helper()
	images := e2eImages{
		mockChain:   os.Getenv("DEVSHARD_E2E_MOCK_CHAIN_IMAGE"),
		host:        os.Getenv("DEVSHARD_E2E_HOST_IMAGE"),
		devshardctl: os.Getenv("DEVSHARD_E2E_DEVSHARDCTL_IMAGE"),
		postgres:    testutil.EnvDefault("DEVSHARD_E2E_POSTGRES_IMAGE", "postgres:18.1-bookworm"),
	}
	var missing []string
	if images.mockChain == "" {
		missing = append(missing, "DEVSHARD_E2E_MOCK_CHAIN_IMAGE")
	}
	if images.host == "" {
		missing = append(missing, "DEVSHARD_E2E_HOST_IMAGE")
	}
	if images.devshardctl == "" {
		missing = append(missing, "DEVSHARD_E2E_DEVSHARDCTL_IMAGE")
	}
	if len(missing) > 0 {
		t.Fatalf("DEVSHARD_E2E=1 requires prebuilt e2e images; missing %s", strings.Join(missing, ", "))
	}
	return images
}
