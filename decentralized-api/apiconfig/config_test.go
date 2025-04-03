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
	"time"
)

type version struct {
	height  int64
	version string
}

func TestNodeVersionStack_PopIf(t *testing.T) {
	tests := []struct {
		name            string
		initialStack    []version
		popHeight       int64
		expectedPops    []string
		secondPopHeight int64
		secondPops      []string
	}{
		{
			name:         "Empty stack",
			initialStack: []version{},
			popHeight:    100,
			expectedPops: []string{},
		},
		{
			name: "Height less than top of stack",
			initialStack: []version{
				{height: 200, version: "v1"},
			},
			popHeight:    100,
			expectedPops: []string{},
		},
		{
			name: "Height equal to top of stack",
			initialStack: []version{
				{height: 200, version: "v1"},
			},
			popHeight:    200,
			expectedPops: []string{"v1"},
		},
		{
			name: "Height greater than top of stack",
			initialStack: []version{
				{height: 200, version: "v1"},
			},
			popHeight:    300,
			expectedPops: []string{"v1"},
		},
		{
			name: "Multiple versions in the stack",
			initialStack: []version{
				{height: 100, version: "v1"},
				{height: 200, version: "v2"},
				{height: 300, version: "v3"},
			},
			popHeight:    250,
			expectedPops: []string{"v2"},
		},
		{
			name: "Multiple versions in the stack, reverse order",
			initialStack: []version{
				{height: 300, version: "v3"},
				{height: 200, version: "v2"},
				{height: 100, version: "v1"},
			},
			popHeight:       250,
			expectedPops:    []string{"v2"},
			secondPopHeight: 0,
			secondPops:      []string{},
		},
		{
			name: "with duplicates",
			initialStack: []version{
				{height: 100, version: "v1"},
				{height: 100, version: "v1"},
				{height: 200, version: "v2"},
				{height: 300, version: "v3"},
			},
			popHeight:       250,
			expectedPops:    []string{"v2"},
			secondPopHeight: 300,
			secondPops:      []string{"v3"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stack := apiconfig.NodeVersionStack{}
			for _, v := range test.initialStack {
				stack.Insert(v.height, v.version)
			}
			for _, version := range test.expectedPops {
				pop, _ := stack.PopIf(test.popHeight)
				require.Equal(t, version, pop)
			}
			_, found := stack.PopIf(test.popHeight)
			require.False(t, found)

			if test.secondPopHeight != 0 {
				for _, version := range test.secondPops {
					pop, _ := stack.PopIf(test.secondPopHeight)
					require.Equal(t, version, pop)
				}
				_, found = stack.PopIf(test.secondPopHeight)
				require.False(t, found)
			}

		})
	}
}

func TestConfigLoad(t *testing.T) {
	testManager := &apiconfig.ConfigManager{
		KoanProvider: rawbytes.Provider([]byte(testYaml)),
	}
	err := testManager.Load()
	require.NoError(t, err)
	require.Equal(t, 8080, testManager.GetApiConfig().Port)
	require.Equal(t, "http://join1-node:26657", testManager.GetChainNodeConfig().Url)
	require.Equal(t, "join1", testManager.GetChainNodeConfig().AccountName)
	require.Equal(t, "test", testManager.GetChainNodeConfig().KeyringBackend)
	require.Equal(t, "/root/.inference", testManager.GetChainNodeConfig().KeyringDir)
}

func TestNodeVersion(t *testing.T) {
	writeCapture := &CaptureWriterProvider{}
	testManager, err := apiconfig.LoadConfigManager(rawbytes.Provider([]byte(testYaml)),
		writeCapture)
	require.NoError(t, err)
	err = testManager.Load()
	require.NoError(t, err)
	require.Equal(t, testManager.GetCurrentNodeVersion(), "")
	err = testManager.AddNodeVersion(50, "v2")
	require.NoError(t, err)
	err = testManager.AddNodeVersion(60, "v3")
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, testManager.GetCurrentNodeVersion(), "")
	err = testManager.SetHeight(50)
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, testManager.GetCurrentNodeVersion(), "v2")
	err = testManager.SetHeight(51)
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, testManager.GetCurrentNodeVersion(), "v2")
	err = testManager.SetHeight(60)
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, testManager.GetCurrentNodeVersion(), "v3")
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
	require.Equal(t, "join2", testManager.GetChainNodeConfig().AccountName)
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
	err = testManager.AddNodeVersion(50, "v1")
	require.NoError(t, err)
	//err = testManager.Write()
	//require.NoError(t, err)

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
	require.Equal(t, "join1", testManager2.GetChainNodeConfig().AccountName)
	require.Equal(t, "test", testManager2.GetChainNodeConfig().KeyringBackend)
	require.Equal(t, "/root/.inference", testManager2.GetChainNodeConfig().KeyringDir)
	require.Equal(t, "v1", testManager2.GetCurrentNodeVersion())
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

func TestChangeConfigLive(t *testing.T) {
	writeCapture := &CaptureWriterProvider{}
	testManager, err := apiconfig.LoadConfigManager(rawbytes.Provider([]byte(testYaml)),
		writeCapture)
	require.NoError(t, err)
	// Set height from 1 to 1000 in succession:
	for i := 1; i <= 1000; i++ {
		println("Setting height to", i)
		err = testManager.SetHeight(int64(i))
		require.NoError(t, err)
	}

	// wait for .1 second
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, int64(1000), testManager.GetHeight())
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
        - unsloth/llama-3-8b-Instruct: {}
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
