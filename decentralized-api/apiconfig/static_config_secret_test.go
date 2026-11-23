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

func TestLoadConfigRemovesLegacyKeyringPassword(t *testing.T) {
	const runtimePassword = "runtime-password"
	t.Setenv("KEYRING_PASSWORD", runtimePassword)
	configPath, databasePath := prepareMigratedConfig(t)
	legacy := `# Node settings
api:
  port: 8080
chain_node:
  url: http://node:26657
  KeyringPassword: |-
    old-password-line-one
    old-password-line-two
other:
  KeyringPassword: keep-other-field
`
	require.NoError(t, os.WriteFile(configPath, []byte(legacy), 0600))
	require.NoError(t, os.Chmod(configPath, 0600))

	manager, err := LoadConfigManagerWithPaths(configPath, databasePath, "")
	require.NoError(t, err)
	require.Equal(t, runtimePassword, manager.GetChainNodeConfig().KeyringPassword)
	require.NoError(t, manager.SqlDb().GetDb().Close())

	cleaned, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.YAMLEq(t, `api:
  port: 8080
chain_node:
  url: http://node:26657
other:
  KeyringPassword: keep-other-field
`, string(cleaned))
	require.Contains(t, string(cleaned), "# Node settings")
	info, err := os.Stat(configPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())

	manager, err = LoadConfigManagerWithPaths(configPath, databasePath, "")
	require.NoError(t, err)
	require.NoError(t, manager.SqlDb().GetDb().Close())
	reread, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Equal(t, cleaned, reread)
	after, err := os.Stat(configPath)
	require.NoError(t, err)
	require.True(t, os.SameFile(info, after))
	require.Equal(t, info.ModTime(), after.ModTime())
}

func TestLoadConfigContinuesWhenLegacyKeyringPasswordCannotBeRemoved(t *testing.T) {
	const runtimePassword = "runtime-password"
	t.Setenv("KEYRING_PASSWORD", runtimePassword)
	configPath, databasePath := prepareMigratedConfig(t)
	legacyPath := filepath.Join(filepath.Dir(configPath), "operator-config.yaml")
	legacy := []byte("chain_node:\n  url: http://node:26657\n  KeyringPassword: old-password\n")
	require.NoError(t, os.WriteFile(legacyPath, legacy, 0600))
	require.NoError(t, os.Remove(configPath))
	require.NoError(t, os.Symlink(legacyPath, configPath))

	manager, err := LoadConfigManagerWithPaths(configPath, databasePath, "")
	require.NoError(t, err)
	require.Equal(t, runtimePassword, manager.GetChainNodeConfig().KeyringPassword)
	require.NoError(t, manager.SqlDb().GetDb().Close())

	target, err := os.Readlink(configPath)
	require.NoError(t, err)
	require.Equal(t, legacyPath, target)
	data, err := os.ReadFile(legacyPath)
	require.NoError(t, err)
	require.Equal(t, legacy, data)
}

func prepareMigratedConfig(t *testing.T) (configPath, databasePath string) {
	t.Helper()
	directory := t.TempDir()
	configPath = filepath.Join(directory, "api-config.yaml")
	databasePath = filepath.Join(directory, "api.db")
	require.NoError(t, os.WriteFile(configPath, []byte("chain_node:\n  url: http://node:26657\n"), 0600))
	manager, err := LoadConfigManagerWithPaths(configPath, databasePath, "")
	require.NoError(t, err)
	require.NoError(t, manager.SqlDb().GetDb().Close())
	return configPath, databasePath
}
