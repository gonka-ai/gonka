package apiconfig

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStaticConfigWriteExcludesKeyringPassword(t *testing.T) {
	const password = "DUMMY_KEYRING_PASSWORD_SENTINEL"
	configPath := filepath.Join(t.TempDir(), "api-config.yaml")
	manager := ConfigManager{
		currentConfig: Config{
			ChainNode: ChainNodeConfig{
				Url:             "http://node:26657",
				KeyringPassword: password,
			},
		},
		WriterProvider: NewFileWriteCloserProvider(configPath),
	}

	require.NoError(t, manager.Write())
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)

	require.Contains(t, string(data), "chain_node:")
	require.Contains(t, string(data), "http://node:26657")
	require.NotContains(t, string(data), password)
	require.NotContains(t, string(data), "KeyringPassword")
	require.Equal(t, password, manager.GetChainNodeConfig().KeyringPassword)
}
