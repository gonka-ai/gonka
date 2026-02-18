package apiconfig_test

import (
	"bytes"
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/logging"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func TestConfigLoad(t *testing.T) {
	testManager := &apiconfig.ConfigManager{
		KoanProvider: rawbytes.Provider([]byte(testYaml)),
	}
	err := testManager.Load()
	require.NoError(t, err)
	require.Equal(t, 8080, testManager.GetApiConfig().Port)
	require.Equal(t, "http://join1-node:26657", testManager.GetChainNodeConfig().Url)
	require.Equal(t, "join1", testManager.GetChainNodeConfig().SignerKeyName)
	require.Equal(t, "test", testManager.GetChainNodeConfig().KeyringBackend)
	require.Equal(t, "/root/.inference", testManager.GetChainNodeConfig().KeyringDir)
}

func TestNodeVersion(t *testing.T) {
	testManager := &apiconfig.ConfigManager{
		KoanProvider:   rawbytes.Provider([]byte(testYaml)),
		WriterProvider: &CaptureWriterProvider{},
	}
	err := testManager.Load()
	require.NoError(t, err)

	// Test that default version is returned correctly
	require.Equal(t, testManager.GetCurrentNodeVersion(), "v3.0.8")
}

func TestSetCurrentNodeVersion(t *testing.T) {
	testManager := &apiconfig.ConfigManager{
		KoanProvider:   rawbytes.Provider([]byte(testYaml)),
		WriterProvider: &CaptureWriterProvider{},
	}
	err := testManager.Load()
	require.NoError(t, err)

	// Initial version from config
	require.Equal(t, "v3.0.8", testManager.GetCurrentNodeVersion())

	// Update version
	err = testManager.SetCurrentNodeVersion("v4.0.0")
	require.NoError(t, err)
	require.Equal(t, "v4.0.0", testManager.GetCurrentNodeVersion())

	// Update to empty version
	err = testManager.SetCurrentNodeVersion("")
	require.NoError(t, err)
	require.Equal(t, "", testManager.GetCurrentNodeVersion())
}

func TestShouldRefreshClients(t *testing.T) {
	testManager := &apiconfig.ConfigManager{
		KoanProvider:   rawbytes.Provider([]byte(testYaml)),
		WriterProvider: &CaptureWriterProvider{},
	}
	err := testManager.Load()
	require.NoError(t, err)

	// Initially, LastUsedVersion is empty and CurrentNodeVersion is "v3.0.8"
	// They differ, so should refresh
	require.True(t, testManager.ShouldRefreshClients())

	// Set LastUsedVersion to match CurrentNodeVersion
	err = testManager.SetLastUsedVersion("v3.0.8")
	require.NoError(t, err)
	require.False(t, testManager.ShouldRefreshClients())

	// Update CurrentNodeVersion - now they differ again
	err = testManager.SetCurrentNodeVersion("v4.0.0")
	require.NoError(t, err)
	require.True(t, testManager.ShouldRefreshClients())

	// Sync LastUsedVersion to match
	err = testManager.SetLastUsedVersion("v4.0.0")
	require.NoError(t, err)
	require.False(t, testManager.ShouldRefreshClients())
}

func TestVersionUpdateTriggersRefresh(t *testing.T) {
	testManager := &apiconfig.ConfigManager{
		KoanProvider:   rawbytes.Provider([]byte(testYaml)),
		WriterProvider: &CaptureWriterProvider{},
	}
	err := testManager.Load()
	require.NoError(t, err)

	// Simulate initial sync: set LastUsedVersion to current
	err = testManager.SetLastUsedVersion(testManager.GetCurrentNodeVersion())
	require.NoError(t, err)
	require.False(t, testManager.ShouldRefreshClients())

	// Simulate chain version update
	newChainVersion := "v5.0.0"
	err = testManager.SetCurrentNodeVersion(newChainVersion)
	require.NoError(t, err)

	// Now ShouldRefreshClients should return true
	require.True(t, testManager.ShouldRefreshClients())

	// After refreshing clients, update LastUsedVersion
	err = testManager.SetLastUsedVersion(newChainVersion)
	require.NoError(t, err)
	require.False(t, testManager.ShouldRefreshClients())
}

type mockCosmosQueryClient struct {
	version string
	err     error
}

func (m *mockCosmosQueryClient) MLNodeVersion(ctx context.Context, req *types.QueryGetMLNodeVersionRequest, opts ...grpc.CallOption) (*types.QueryGetMLNodeVersionResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &types.QueryGetMLNodeVersionResponse{
		MlnodeVersion: types.MLNodeVersion{
			CurrentVersion: m.version,
		},
	}, nil
}

func TestSyncVersionFromChain_UpdatesWhenDifferent(t *testing.T) {
	testManager := &apiconfig.ConfigManager{
		KoanProvider:   rawbytes.Provider([]byte(testYaml)),
		WriterProvider: &CaptureWriterProvider{},
	}
	err := testManager.Load()
	require.NoError(t, err)

	// Initial version from config
	require.Equal(t, "v3.0.8", testManager.GetCurrentNodeVersion())

	// Sync with chain that has a different version
	mockClient := &mockCosmosQueryClient{version: "v4.0.0"}
	err = testManager.SyncVersionFromChain(mockClient)
	require.NoError(t, err)

	// Version should be updated to chain version
	require.Equal(t, "v4.0.0", testManager.GetCurrentNodeVersion())
}

func TestSyncVersionFromChain_NoUpdateWhenSame(t *testing.T) {
	testManager := &apiconfig.ConfigManager{
		KoanProvider:   rawbytes.Provider([]byte(testYaml)),
		WriterProvider: &CaptureWriterProvider{},
	}
	err := testManager.Load()
	require.NoError(t, err)

	// Initial version from config
	require.Equal(t, "v3.0.8", testManager.GetCurrentNodeVersion())

	// Sync with chain that has the same version
	mockClient := &mockCosmosQueryClient{version: "v3.0.8"}
	err = testManager.SyncVersionFromChain(mockClient)
	require.NoError(t, err)

	// Version should remain the same
	require.Equal(t, "v3.0.8", testManager.GetCurrentNodeVersion())
}

func TestSyncVersionFromChain_ErrorKeepsCurrentVersion(t *testing.T) {
	testManager := &apiconfig.ConfigManager{
		KoanProvider:   rawbytes.Provider([]byte(testYaml)),
		WriterProvider: &CaptureWriterProvider{},
	}
	err := testManager.Load()
	require.NoError(t, err)

	// Initial version from config
	require.Equal(t, "v3.0.8", testManager.GetCurrentNodeVersion())

	// Sync with chain that returns an error
	mockClient := &mockCosmosQueryClient{err: errors.New("chain unavailable")}
	err = testManager.SyncVersionFromChain(mockClient)
	require.Error(t, err)

	// Version should remain unchanged
	require.Equal(t, "v3.0.8", testManager.GetCurrentNodeVersion())
}

func TestConfigLoadEnvOverride(t *testing.T) {
	testManager := &apiconfig.ConfigManager{
		KoanProvider: rawbytes.Provider([]byte(testYaml)),
	}

	os.Setenv("DAPI_API__PORT", "9000")
	os.Setenv("KEY_NAME", "join2")
	os.Setenv("DAPI_CHAIN_NODE__URL", "http://join1-node:26658")
	os.Setenv("DAPI_API__POC_CALLBACK_URL", "http://callback")
	os.Setenv("DAPI_API__PUBLIC_URL", "http://public")
	err := testManager.Load()
	require.NoError(t, err)
	require.Equal(t, 9000, testManager.GetApiConfig().Port)
	require.Equal(t, "http://join1-node:26658", testManager.GetChainNodeConfig().Url)
	require.Equal(t, "join2", testManager.GetChainNodeConfig().SignerKeyName)
	require.Equal(t, "http://callback", testManager.GetApiConfig().PoCCallbackUrl)
	require.Equal(t, "http://public", testManager.GetApiConfig().PublicUrl)
	require.Equal(t, "test", testManager.GetChainNodeConfig().KeyringBackend)
	require.Equal(t, "/root/.inference", testManager.GetChainNodeConfig().KeyringDir)

}

type CaptureWriterProvider struct {
	CapturedData string
}

func (c *CaptureWriterProvider) Write(data []byte) (int, error) {
	c.CapturedData += string(data)
	return len(data), nil
}

func (c *CaptureWriterProvider) Close() error {
	return nil
}

func (c *CaptureWriterProvider) GetWriter() apiconfig.WriteCloser {
	return c
}

func TestConfigRoundTrip(t *testing.T) {
	os.Unsetenv("DAPI_API__PORT")
	os.Unsetenv("KEY_NAME")
	os.Unsetenv("DAPI_CHAIN_NODE__URL")
	os.Unsetenv("DAPI_API__POC_CALLBACK_URL")
	os.Unsetenv("DAPI_API__PUBLIC_URL")
	writeCapture := &CaptureWriterProvider{}
	testManager := &apiconfig.ConfigManager{
		KoanProvider:   rawbytes.Provider([]byte(testYaml)),
		WriterProvider: writeCapture,
	}
	err := testManager.Load()
	require.NoError(t, err)

	// Test can write config successfully
	err = testManager.Write()
	require.NoError(t, err)

	t.Log("\n")
	t.Log(writeCapture.CapturedData)
	testManager2 := &apiconfig.ConfigManager{
		KoanProvider:   rawbytes.Provider([]byte(writeCapture.CapturedData)),
		WriterProvider: writeCapture,
	}
	err = testManager2.Load()

	testManager2.SetHeight(50)
	require.NoError(t, err)
	require.Equal(t, 8080, testManager2.GetApiConfig().Port)
	require.Equal(t, "http://join1-node:26657", testManager2.GetChainNodeConfig().Url)
	require.Equal(t, "join1", testManager2.GetChainNodeConfig().SignerKeyName)
	require.Equal(t, "test", testManager2.GetChainNodeConfig().KeyringBackend)
	require.Equal(t, "/root/.inference", testManager2.GetChainNodeConfig().KeyringDir)
	// After Write() we persist only static fields; dynamic fields like version are not in YAML
	require.Equal(t, "", testManager2.GetCurrentNodeVersion())
}

func writeTemp(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	return path
}

func loadManager(t *testing.T) error {
	testManager := &apiconfig.ConfigManager{
		KoanProvider: rawbytes.Provider([]byte(testYaml)),
	}
	os.Setenv("DAPI_API__PORT", "9000")
	os.Setenv("DAPI_CHAIN_NODE__URL", "http://join1-node:26658")
	os.Setenv("KEY_NAME", "join2")
	defer func() {
		// Clean up environment variables
		os.Unsetenv("DAPI_API__PORT")
		os.Unsetenv("DAPI_CHAIN_NODE__URL")
		os.Unsetenv("KEY_NAME")
	}()

	if err := testManager.Load(); err != nil {
		return err
	}
	return nil
}

// We cannot write anything to stdout when loading config or we break cosmovisor!
func TestNoLoggingToStdout(t *testing.T) {
	// Save the original stdout
	originalStdout := os.Stdout
	defer func() { os.Stdout = originalStdout }() // Restore it after the test

	// Create a pipe to capture stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Failed to create pipe: %v", err)
	}
	os.Stdout = w

	// Buffer to capture log output
	var buf bytes.Buffer

	// Load config with overrides
	_, noLoggerErr := logging.WithNoopLogger(func() (interface{}, error) {
		err := loadManager(t)
		return nil, err
	})
	require.NoError(t, noLoggerErr)

	// Close the pipe and reset stdout
	_ = w.Close()
	os.Stdout = originalStdout

	// Read captured output
	_, _ = buf.ReadFrom(r)

	// Fail if anything is written to stdout
	if buf.Len() > 0 {
		t.Errorf("Unexpected logging to stdout: %q", buf.String())
	}
}

var testYaml = `
api:
    port: 8080
chain_node:
    url: http://join1-node:26657
    signer_key_name: join1
    account_public_key: ""
    keyring_backend: test
    keyring_dir: /root/.inference
current_height: 393
current_seed:
    seed: 3898730504561900192
    height: 380
    signature: 815794b7bbb414900a84c8a543ffc96a3ebb5fbbd0175648eaf5f60897b786df5a0be5bc6047ee2ac3c8c2444510fcb9a1f565a6359927226f619dd534035bb7
nodes:
    - url: http://34.171.235.205:8080/
      models:
        - Qwen/Qwen2.5-7B-Instruct: {}
      id: node1
      max_concurrent: 500
previous_seed:
    seed: 1370553182438852893
    height: 370
    signature: 1d1f9fc6f44840af03368ce24e0335834181e42a9a45c81d7a17e14866729fa81a08c14d3397e00d4b16da3ab708e284650f8b14b33b318820ae0524b6ead6db
upcoming_seed:
    seed: 254929314898674592
    height: 390
    signature: 75296c164d43e5570c44c88176c7988e7d52d3e44be6c43e8e6c8f07327279510092f429addc401665d6ed128725f2181a95c7aba66c89ea77209c55ef2ce342
upgrade_plan:
    name: ""
    height: 0
    binaries: {}
current_node_version: "v3.0.8"
`
