package apiconfig_test

import (
	"bytes"
	"decentralized-api/apiconfig"
	"decentralized-api/logging"
	"fmt"
	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
)

func TestConfigLoad(t *testing.T) {
	testManager := &apiconfig.ConfigManager{
		KoanProvider: rawbytes.Provider([]byte(testYaml)),
	}
	err := testManager.Load()
	require.NoError(t, err)
	require.Equal(t, 8080, testManager.GetConfig().Api.Port)
	require.Equal(t, "http://join1-node:26657", testManager.GetConfig().ChainNode.Url)
	require.Equal(t, "join1", testManager.GetConfig().ChainNode.AccountName)
	require.Equal(t, "test", testManager.GetConfig().ChainNode.KeyringBackend)
	require.Equal(t, "/root/.inference", testManager.GetConfig().ChainNode.KeyringDir)
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
	require.Equal(t, 9000, testManager.GetConfig().Api.Port)
	require.Equal(t, "http://join1-node:26658", testManager.GetConfig().ChainNode.Url)
	require.Equal(t, "join2", testManager.GetConfig().ChainNode.AccountName)
	require.Equal(t, "http://callback", testManager.GetConfig().Api.PoCCallbackUrl)
	require.Equal(t, "http://public", testManager.GetConfig().Api.PublicUrl)
	require.Equal(t, "test", testManager.GetConfig().ChainNode.KeyringBackend)
	require.Equal(t, "/root/.inference", testManager.GetConfig().ChainNode.KeyringDir)

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

	err = testManager.Write()
	require.NoError(t, err)

	t.Log("\n")
	t.Log(writeCapture.CapturedData)
	testManager2 := &apiconfig.ConfigManager{
		KoanProvider: rawbytes.Provider([]byte(writeCapture.CapturedData)),
	}
	err = testManager2.Load()

	require.NoError(t, err)
	require.Equal(t, 8080, testManager2.GetConfig().Api.Port)
	require.Equal(t, "http://join1-node:26657", testManager2.GetConfig().ChainNode.Url)
	require.Equal(t, "join1", testManager2.GetConfig().ChainNode.AccountName)
	require.Equal(t, "test", testManager2.GetConfig().ChainNode.KeyringBackend)
	require.Equal(t, "/root/.inference", testManager2.GetConfig().ChainNode.KeyringDir)
}

func loadManager(t *testing.T, err error) error {
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

	err = testManager.Load()
	require.NoError(t, err)
	return err
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
	_, err = logging.WithNoopLogger(func() (interface{}, error) {
		err := loadManager(t, err)
		return nil, err
	})

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

// Example function in the library
func LibraryFunctionThatShouldNotLog() {
	// Simulate a log that should not reach stdout
	//logging.Info("Oops, this log should fail the test")
	fmt.Println("This should fail the test")
}

var testYaml = `
api:
    port: 8080
chain_node:
    url: http://join1-node:26657
    account_name: join1
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
        - unsloth/llama-3-8b-Instruct
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
`
