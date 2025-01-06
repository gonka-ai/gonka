package apiconfig_test

import (
	"decentralized-api/apiconfig"
	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/stretchr/testify/require"
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
