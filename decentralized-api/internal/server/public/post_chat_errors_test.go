package public

import (
	"os"
	"testing"

	"decentralized-api/apiconfig"

	"github.com/knadh/koanf/providers/file"
	"github.com/stretchr/testify/require"
)

func newTestConfigManager(t *testing.T) *apiconfig.ConfigManager {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "config-*.yaml")
	require.NoError(t, err)
	t.Cleanup(func() { os.Remove(tmpFile.Name()) })

	_, err = tmpFile.Write([]byte("nodes: []"))
	require.NoError(t, err)
	require.NoError(t, tmpFile.Close())

	configManager := &apiconfig.ConfigManager{
		KoanProvider:   file.Provider(tmpFile.Name()),
		WriterProvider: apiconfig.NewFileWriteCloserProvider(tmpFile.Name()),
	}
	require.NoError(t, configManager.Load())
	return configManager
}
