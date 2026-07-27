package e2e

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tclog "github.com/testcontainers/testcontainers-go/log"
	"github.com/testcontainers/testcontainers-go/wait"

	"devshard/e2e/testutil"
)

type quietTestcontainersLogger struct{}

func (quietTestcontainersLogger) Printf(string, ...any) {}

func init() {
	tclog.SetDefault(quietTestcontainersLogger{})
}

type e2eEnv struct {
	networkName string
	network     testcontainers.Network
	containers  []namedContainer
	clientURL   string

	images          e2eImages
	hostURLs        []string
	hostControlURLs []string
	hostVolumeNames []string
	usePostgres     bool
}

type namedContainer struct {
	name      string
	container testcontainers.Container
}

type containerSpec struct {
	name              string
	image             string
	port              string
	aliases           []string
	env               map[string]string
	tmpfs             map[string]string
	waitPath          string
	waitLog           string
	waitLogOccurrence int
	mounts            []mount.Mount
}

type e2eEnvOptions struct {
	hostVolumeNames    []string
	usePostgresStorage bool
}

func startHappyPathEnv(ctx context.Context, t *testing.T, images e2eImages) *e2eEnv {
	t.Helper()
	return startE2EEnv(ctx, t, images, e2eEnvOptions{})
}

func startE2EEnv(ctx context.Context, t *testing.T, images e2eImages, opts e2eEnvOptions) *e2eEnv {
	t.Helper()
	testutil.DebugLogf(t, "E2E images: mock-chain=%s host=%s devshardctl=%s postgres=%s",
		images.mockChain, images.host, images.devshardctl, images.postgres)

	networkName := fmt.Sprintf("devshard-e2e-%s-%d",
		strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name())),
		time.Now().UnixNano(),
	)
	testutil.DebugLogf(t, "creating Docker network %s", networkName)
	network, err := testcontainers.GenericNetwork(ctx, testcontainers.GenericNetworkRequest{
		NetworkRequest: testcontainers.NetworkRequest{
			Name:           networkName,
			CheckDuplicate: true,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = network.Remove(context.Background()) })

	env := &e2eEnv{
		networkName:     networkName,
		network:         network,
		images:          images,
		hostVolumeNames: opts.hostVolumeNames,
		usePostgres:     opts.usePostgresStorage,
	}
	if len(opts.hostVolumeNames) > 0 {
		t.Cleanup(func() { removeDockerVolumes(context.Background(), t, opts.hostVolumeNames) })
	}
	t.Cleanup(func() { env.terminate(context.Background(), t) })

	mockChain := env.startContainer(ctx, t, containerSpec{
		name:    mockChainAlias,
		image:   images.mockChain,
		port:    "9090/tcp",
		aliases: []string{mockChainAlias},
		waitLog: "mock-chain gRPC listening",
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
		waitLogOccurrence: 2,
	})

	env.hostURLs = make([]string, 3)
	env.hostControlURLs = make([]string, len(env.hostURLs))
	for i := range env.hostURLs {
		env.hostURLs[i] = fmt.Sprintf("http://devshard-host-%d:8080", i)
	}
	if env.usePostgres {
		env.createPostgresHostDatabases(ctx, t, postgres)
	}
	for i := range env.hostURLs {
		env.hostControlURLs[i] = containerURL(ctx, t, env.startHost(ctx, t, i), "8080/tcp")
	}

	devshardctl := env.startContainer(ctx, t, containerSpec{
		name:    devshardCtlName,
		image:   images.devshardctl,
		port:    "8080/tcp",
		aliases: []string{devshardCtlName},
		env: map[string]string{
			"DEVSHARD_ESCROW_ID":       defaultEscrowID,
			"DEVSHARD_CHAIN_GRPC":      mockChainAlias + ":9090",
			"DEVSHARD_PUBLIC_API":      "http://" + mockChainAlias + ":9191",
			"DEVSHARD_PARAMS_SOURCE":   "chain",
			"DEVSHARD_PRIVATE_KEY":     testutil.EnvDefault("DEVSHARD_E2E_USER_PRIVATE_KEY", testutil.UserPrivateKey),
			"DEVSHARD_ADMIN_API_KEY":   testutil.AdminAPIKey,
			"DEVSHARD_STORAGE_PATH":    "/tmp/devshardctl",
			"DEVSHARD_MODEL":           "stub-model",
			"GATEWAY_MAX_TOKENS_CAP":   "4096",
		},
		waitPath: "/v1/status",
	})

	env.clientURL = containerURL(ctx, t, devshardctl, "8080/tcp")
	testutil.DebugLogf(t, "devshardctl client URL: %s", env.clientURL)

	require.NotNil(t, mockChain)
	require.NotNil(t, postgres)
	return env
}

func hostName(index int) string {
	return fmt.Sprintf("devshard-host-%d", index)
}

func postgresHostDatabaseName(index int) string {
	return fmt.Sprintf("devshard_host_%d", index)
}

func (e *e2eEnv) createPostgresHostDatabases(ctx context.Context, t *testing.T, postgres testcontainers.Container) {
	t.Helper()
	for i := range e.hostURLs {
		dbName := postgresHostDatabaseName(i)
		code, output, err := postgres.Exec(ctx, []string{"createdb", "-U", "devshard", dbName})
		var body []byte
		if output != nil {
			body, _ = io.ReadAll(output)
		}
		require.NoError(t, err, "create postgres database %s: %s", dbName, string(body))
		require.Equal(t, 0, code, "create postgres database %s: %s", dbName, string(body))
		testutil.DebugLogf(t, "created Postgres database %s for %s", dbName, hostName(i))
	}
}

// e2eHostSessionEnv returns escrow session fields that must match
// e2e/mock-chain-config.yaml so devshardctl (chain-backed) and hosts agree.
func e2eHostSessionEnv() map[string]string {
	return map[string]string{
		"DEVSHARD_TOKEN_PRICE":                  "7",
		"DEVSHARD_CREATE_DEVSHARD_FEE":          "12345",
		"DEVSHARD_FEE_PER_NONCE":                "19",
		"DEVSHARD_VALIDATION_RATE":              "10000",
		"DEVSHARD_VOTE_THRESHOLD_FACTOR":        "66",
		"DEVSHARD_INFERENCE_SEAL_GRACE_NONCES":  "9",
		"DEVSHARD_INFERENCE_SEAL_GRACE_SECONDS": "77",
		"DEVSHARD_AUTO_SEAL_EVERY_N_NONCES":     "21",
	}
}

func (e *e2eEnv) startHost(ctx context.Context, t *testing.T, index int) testcontainers.Container {
	t.Helper()
	env := map[string]string{
		"DEVSHARD_ESCROW_ID":         defaultEscrowID,
		"DEVSHARD_HOST_INDEX":        fmt.Sprintf("%d", index),
		"DEVSHARD_HOST_PRIVATE_KEYS": strings.Join(testutil.HostPrivateKeys, ","),
		"DEVSHARD_USER_PRIVATE_KEY":  testutil.UserPrivateKey,
		"DEVSHARD_PEER_URLS":         strings.Join(e.hostURLs, ","),
		"DEVSHARD_STUB_INFERENCE":    "1",
	}
	for k, v := range e2eHostSessionEnv() {
		env[k] = v
	}
	var mounts []mount.Mount
	if index < len(e.hostVolumeNames) && e.hostVolumeNames[index] != "" {
		env["DEVSHARD_DATA_DIR"] = "/data/devshard-host"
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeVolume,
			Source: e.hostVolumeNames[index],
			Target: "/data",
		})
	}
	if e.usePostgres {
		env["PGHOST"] = postgresAlias
		env["PGPORT"] = "5432"
		env["PGDATABASE"] = postgresHostDatabaseName(index)
		env["PGUSER"] = "devshard"
		env["PGPASSWORD"] = "devshard"
		env["PG_CONNECT_TIMEOUT"] = "10s"
	}
	return e.startContainer(ctx, t, containerSpec{
		name:     hostName(index),
		image:    e.images.host,
		port:     "8080/tcp",
		aliases:  []string{hostName(index)},
		env:      env,
		mounts:   mounts,
		waitPath: "/health",
	})
}

func (e *e2eEnv) restartHost(ctx context.Context, t *testing.T, index int) {
	t.Helper()
	name := hostName(index)
	testutil.DebugLogf(t, "restarting %s", name)
	e.stopHost(ctx, t, index)
	e.hostControlURLs[index] = containerURL(ctx, t, e.startHost(ctx, t, index), "8080/tcp")
}

func containerURL(ctx context.Context, t *testing.T, container testcontainers.Container, portName string) string {
	t.Helper()
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, nat.Port(portName))
	require.NoError(t, err)
	return "http://" + host + ":" + port.Port()
}

func (e *e2eEnv) stopHost(ctx context.Context, t *testing.T, index int) {
	t.Helper()
	name := hostName(index)
	testutil.DebugLogf(t, "stopping %s", name)
	for i := range e.containers {
		if e.containers[i].name != name {
			continue
		}
		require.NoError(t, e.containers[i].container.Terminate(ctx), "terminate %s", name)
		e.containers = append(e.containers[:i], e.containers[i+1:]...)
		return
	}
	t.Fatalf("container %s not found", name)
}

func (e *e2eEnv) restartAllHosts(ctx context.Context, t *testing.T) {
	t.Helper()
	for i := range e.hostURLs {
		e.restartHost(ctx, t, i)
	}
}

func (e *e2eEnv) startContainer(ctx context.Context, t *testing.T, spec containerSpec) testcontainers.Container {
	t.Helper()
	testutil.DebugLogf(t, "starting container %s image=%s aliases=%s port=%s waitPath=%s waitLog=%q",
		spec.name, spec.image, strings.Join(spec.aliases, ","), spec.port, spec.waitPath, spec.waitLog)

	var waitStrategy wait.Strategy
	if spec.waitLog != "" {
		occurrence := spec.waitLogOccurrence
		if occurrence <= 0 {
			occurrence = 1
		}
		waitStrategy = wait.ForLog(spec.waitLog).
			WithOccurrence(occurrence).
			WithStartupTimeout(testutil.DefaultRequestTimeout)
	} else {
		waitStrategy = wait.ForHTTP(spec.waitPath).
			WithPort(nat.Port(spec.port)).
			WithStartupTimeout(testutil.DefaultRequestTimeout)
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:          spec.image,
			Env:            spec.env,
			ExposedPorts:   []string{spec.port},
			Networks:       []string{e.networkName},
			NetworkAliases: map[string][]string{e.networkName: spec.aliases},
			Tmpfs:          spec.tmpfs,
			HostConfigModifier: func(hostConfig *container.HostConfig) {
				hostConfig.Mounts = append(hostConfig.Mounts, spec.mounts...)
			},
			WaitingFor: waitStrategy,
		},
		Started: true,
		Logger:  quietTestcontainersLogger{},
	})
	if err != nil {
		if container != nil {
			dumpContainerLogs(ctx, t, spec.name, container)
			_ = container.Terminate(context.Background())
		}
		t.Fatalf("start %s container from image %s: %v", spec.name, spec.image, err)
	}
	e.containers = append(e.containers, namedContainer{name: spec.name, container: container})
	testutil.DebugLogf(t, "container %s is ready", spec.name)
	return container
}

func sqliteHostVolumeNames(t *testing.T) []string {
	t.Helper()
	base := strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name()))
	names := make([]string, 3)
	for i := range names {
		names[i] = fmt.Sprintf("devshard-e2e-%s-sqlite-%d", base, i)
	}
	return names
}

func removeDockerVolumes(ctx context.Context, t *testing.T, names []string) {
	t.Helper()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Logf("create docker client for volume cleanup: %v", err)
		return
	}
	defer cli.Close()
	for _, name := range names {
		if name == "" {
			continue
		}
		if err := cli.VolumeRemove(ctx, name, true); err != nil {
			t.Logf("remove docker volume %s: %v", name, err)
		}
	}
}

func (e *e2eEnv) terminate(ctx context.Context, t *testing.T) {
	t.Helper()
	if t.Failed() && testutil.DebugEnabled() {
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
		dumpContainerLogs(ctx, t, c.name, c.container)
	}
}

func dumpContainerLogs(ctx context.Context, t *testing.T, name string, c testcontainers.Container) {
	t.Helper()
	logs, err := c.Logs(ctx)
	if err != nil {
		t.Logf("debug logs for %s unavailable: %v", name, err)
		return
	}
	body, readErr := io.ReadAll(logs)
	if closeErr := logs.Close(); closeErr != nil {
		t.Logf("close debug logs for %s: %v", name, closeErr)
	}
	if readErr != nil {
		t.Logf("read debug logs for %s: %v", name, readErr)
		return
	}
	t.Logf("debug logs for %s:\n%s", name, string(body))
}
