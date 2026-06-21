package e2e

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/docker/go-connections/nat"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

type e2eEnv struct {
	networkName string
	network     testcontainers.Network
	containers  []namedContainer
	clientURL   string
}

type namedContainer struct {
	name      string
	container testcontainers.Container
}

type containerSpec struct {
	name     string
	image    string
	port     string
	aliases  []string
	env      map[string]string
	tmpfs    map[string]string
	waitPath string
	waitLog  string
}

func startHappyPathEnv(ctx context.Context, t *testing.T, images e2eImages) *e2eEnv {
	t.Helper()
	debugLogf(t, "E2E images: mock-chain=%s host=%s devshardctl=%s postgres=%s",
		images.mockChain, images.host, images.devshardctl, images.postgres)

	networkName := "devshard-e2e-" + strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name()))
	debugLogf(t, "creating Docker network %s", networkName)
	network, err := testcontainers.GenericNetwork(ctx, testcontainers.GenericNetworkRequest{
		NetworkRequest: testcontainers.NetworkRequest{
			Name:           networkName,
			CheckDuplicate: true,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = network.Remove(context.Background()) })

	env := &e2eEnv{
		networkName: networkName,
		network:     network,
	}
	t.Cleanup(func() { env.terminate(context.Background(), t) })

	mockChain := env.startContainer(ctx, t, containerSpec{
		name:    mockChainAlias,
		image:   images.mockChain,
		port:    "8080/tcp",
		aliases: []string{mockChainAlias},
		env: map[string]string{
			"MOCK_CHAIN_PORT":              "8080",
			"DEVSHARD_E2E_ESCROW_ID":       defaultEscrowID,
			"DEVSHARD_E2E_CREATOR_ADDRESS": signerAddress(t, e2eUserPrivateKey),
			"DEVSHARD_E2E_HOSTS":           e2eHostList(t),
		},
		waitPath: "/health",
	})

	postgres := env.startContainer(ctx, t, containerSpec{
		name:    postgresAlias,
		image:   images.postgres,
		port:    "5432/tcp",
		aliases: []string{postgresAlias},
		env: map[string]string{
			"POSTGRES_DB":       "devshard",
			"POSTGRES_USER":     "devshard",
			"POSTGRES_PASSWORD": "devshard",
			"PGDATA":            "/tmp/pgdata",
		},
		tmpfs: map[string]string{
			"/tmp/pgdata": "rw",
		},
		waitLog: "database system is ready to accept connections",
	})

	hostURLs := make([]string, 3)
	for i := range hostURLs {
		hostURLs[i] = fmt.Sprintf("http://devshard-host-%d:8080", i)
	}
	for i := range hostURLs {
		env.startContainer(ctx, t, containerSpec{
			name:    fmt.Sprintf("devshard-host-%d", i),
			image:   images.host,
			port:    "8080/tcp",
			aliases: []string{fmt.Sprintf("devshard-host-%d", i)},
			env: map[string]string{
				"DEVSHARD_E2E_MODE":          "1",
				"DEVSHARD_ESCROW_ID":         defaultEscrowID,
				"DEVSHARD_HOST_INDEX":        fmt.Sprintf("%d", i),
				"DEVSHARD_HOST_PRIVATE_KEYS": strings.Join(e2eHostPrivateKeys, ","),
				"DEVSHARD_USER_PRIVATE_KEY":  e2eUserPrivateKey,
				"DEVSHARD_CHAIN_REST":        "http://" + mockChainAlias + ":8080",
				"DEVSHARD_PUBLIC_API":        "http://" + mockChainAlias + ":8080",
				"DEVSHARD_PEER_URLS":         strings.Join(hostURLs, ","),
				"DEVSHARD_STUB_INFERENCE":    "1",
			},
			waitPath: "/health",
		})
	}

	devshardctl := env.startContainer(ctx, t, containerSpec{
		name:    devshardCtlName,
		image:   images.devshardctl,
		port:    "8080/tcp",
		aliases: []string{devshardCtlName},
		env: map[string]string{
			"DEVSHARD_E2E_MODE":      "1",
			"DEVSHARD_ESCROW_ID":     defaultEscrowID,
			"DEVSHARD_CHAIN_REST":    "http://" + mockChainAlias + ":8080",
			"DEVSHARD_PUBLIC_API":    "http://" + mockChainAlias + ":8080",
			"DEVSHARD_HOST_URLS":     strings.Join(hostURLs, ","),
			"DEVSHARD_PRIVATE_KEY":   envDefault("DEVSHARD_E2E_USER_PRIVATE_KEY", e2eUserPrivateKey),
			"DEVSHARD_ADMIN_API_KEY": e2eAdminAPIKey,
			"DEVSHARD_STORAGE_PATH":  "/tmp/devshardctl",
			"DEVSHARD_MODEL":         "stub-model",
			"GATEWAY_MAX_TOKENS_CAP": "4096",
		},
		waitPath: "/v1/status",
	})

	host, err := devshardctl.Host(ctx)
	require.NoError(t, err)
	port, err := devshardctl.MappedPort(ctx, "8080/tcp")
	require.NoError(t, err)
	env.clientURL = "http://" + host + ":" + port.Port()
	debugLogf(t, "devshardctl client URL: %s", env.clientURL)

	require.NotNil(t, mockChain)
	require.NotNil(t, postgres)
	return env
}

func e2eHostList(t *testing.T) string {
	t.Helper()
	entries := make([]string, len(e2eHostPrivateKeys))
	for i, key := range e2eHostPrivateKeys {
		entries[i] = fmt.Sprintf("%s=http://devshard-host-%d:8080", signerAddress(t, key), i)
	}
	return strings.Join(entries, ",")
}

func (e *e2eEnv) startContainer(ctx context.Context, t *testing.T, spec containerSpec) testcontainers.Container {
	t.Helper()
	debugLogf(t, "starting container %s image=%s aliases=%s port=%s waitPath=%s waitLog=%q",
		spec.name, spec.image, strings.Join(spec.aliases, ","), spec.port, spec.waitPath, spec.waitLog)

	var waitStrategy wait.Strategy
	if spec.waitLog != "" {
		waitStrategy = wait.ForLog(spec.waitLog).WithOccurrence(2).WithStartupTimeout(defaultRequestTimeout)
	} else {
		waitStrategy = wait.ForHTTP(spec.waitPath).WithPort(nat.Port(spec.port)).WithStartupTimeout(defaultRequestTimeout)
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:          spec.image,
			Env:            spec.env,
			ExposedPorts:   []string{spec.port},
			Networks:       []string{e.networkName},
			NetworkAliases: map[string][]string{e.networkName: spec.aliases},
			Tmpfs:          spec.tmpfs,
			WaitingFor:     waitStrategy,
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start %s container from image %s: %v", spec.name, spec.image, err)
	}
	e.containers = append(e.containers, namedContainer{name: spec.name, container: container})
	debugLogf(t, "container %s is ready", spec.name)
	return container
}

func (e *e2eEnv) terminate(ctx context.Context, t *testing.T) {
	t.Helper()
	if t.Failed() && e2eDebugEnabled() {
		e.dumpContainerLogs(ctx, t)
	}
	for i := len(e.containers) - 1; i >= 0; i-- {
		c := e.containers[i]
		if err := c.container.Terminate(ctx); err != nil {
			t.Logf("terminate %s: %v", c.name, err)
		}
	}
}

func (e *e2eEnv) dumpContainerLogs(ctx context.Context, t *testing.T) {
	t.Helper()
	for i := len(e.containers) - 1; i >= 0; i-- {
		c := e.containers[i]
		logs, err := c.container.Logs(ctx)
		if err != nil {
			t.Logf("debug logs for %s unavailable: %v", c.name, err)
			continue
		}
		body, readErr := io.ReadAll(logs)
		if closeErr := logs.Close(); closeErr != nil {
			t.Logf("close debug logs for %s: %v", c.name, closeErr)
		}
		if readErr != nil {
			t.Logf("read debug logs for %s: %v", c.name, readErr)
			continue
		}
		t.Logf("debug logs for %s:\n%s", c.name, string(body))
	}
}
