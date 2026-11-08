package e2e

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

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

// waitNextTrafficSlot waits until the next assigned executor is wantSlot.
// Bootstrap heartbeats raise the floor before chat, so the next executor is
// whichever slot the cadence has reached.
func waitNextTrafficSlot(t *testing.T, client *http.Client, clientURL string, hostCount, wantSlot int) int {
	t.Helper()
	require.Greater(t, hostCount, 0)
	var nextSlot int
	deadline := time.Now().Add(90 * time.Second)
	for {
		nextSlot = int((testutil.LatestSessionNonce(t, client, clientURL) + 1) % uint64(hostCount))
		if nextSlot == wantSlot || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Second)
	}
	require.Equal(t, wantSlot, nextSlot, "test setup expects slot %d to own the next traffic nonce", wantSlot)
	return nextSlot
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
