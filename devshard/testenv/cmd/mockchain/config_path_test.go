package main

import (
	"os"
	"testing"

	"devshard/testenv/mockchain/seed"

	"github.com/stretchr/testify/require"
)

func TestConfigPathAlias(t *testing.T) {
	dir := t.TempDir()
	cfgPath := dir + "/config.yaml"
	require.NoError(t, os.WriteFile(cfgPath, []byte(`chain_id: alias-test
participants:
  - address: gonka1abc
    inference_url: http://example
escrows:
  - id: 1
    creator: gonka1abc
    amount: 1
    slots: [http://example]
    model_id: m
`), 0o644))

	t.Setenv("MOCK_CHAIN_CONFIG", "")
	t.Setenv("CONFIG_PATH", cfgPath)

	got := os.Getenv("MOCK_CHAIN_CONFIG")
	if got == "" {
		got = os.Getenv("CONFIG_PATH")
	}
	st, err := seed.Load(got)
	require.NoError(t, err)
	require.Equal(t, "alias-test", st.GetChainID())
}
