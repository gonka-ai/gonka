package cmd

import (
	"bytes"
	"testing"
	"text/template"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/app"
)

// TestAppConfigTemplateHasBeaconSection renders app.toml the way `init` does
// and reads it back: the [bls-beacon] section exists with its defaults.
func TestAppConfigTemplateHasBeaconSection(t *testing.T) {
	tmplText, cfg := initAppConfig()
	tmpl, err := template.New("app.toml").Parse(tmplText)
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, tmpl.Execute(&buf, cfg))
	require.Contains(t, buf.String(), "[bls-beacon]")

	v := viper.New()
	v.SetConfigType("toml")
	require.NoError(t, v.ReadConfig(&buf))
	require.True(t, v.GetBool(app.FlagBeaconEnabled))
	require.Equal(t, "", v.GetString(app.FlagBeaconShareDir))
	// The SDK sections are still there.
	require.NotEmpty(t, v.GetString("minimum-gas-prices") + v.GetString("pruning"))
}
