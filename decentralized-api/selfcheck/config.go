package selfcheck

import (
	"decentralized-api/apiconfig"
	"os"

	"github.com/knadh/koanf/providers/file"
)

// newInMemoryConfig builds a ConfigManager backed by a tmpfile, used
// only during selfcheck. Returns the manager plus a cleanup func that
// removes the tmpfile.
func newInMemoryConfig() (*apiconfig.ConfigManager, func(), error) {
	tmp, err := os.CreateTemp("", "selfcheck-config-*.yaml")
	if err != nil {
		return nil, nil, err
	}
	if _, err := tmp.Write([]byte("nodes: []")); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, nil, err
	}
	tmp.Close()

	cm := &apiconfig.ConfigManager{
		KoanProvider:   file.Provider(tmp.Name()),
		WriterProvider: apiconfig.NewFileWriteCloserProvider(tmp.Name()),
	}
	if err := cm.Load(); err != nil {
		os.Remove(tmp.Name())
		return nil, nil, err
	}
	cleanup := func() { os.Remove(tmp.Name()) }
	return cm, cleanup, nil
}
