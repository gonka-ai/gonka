package cmd

import (
	"testing"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestAppDBMaxOpenFiles(t *testing.T) {
	defer func(o dbm.Options) { appDBOptions = o }(appDBOptions)

	appDBOptions = nil
	require.GreaterOrEqual(t, appDBMaxOpenFiles(), goleveldbDefaultOpenFiles)

	v := viper.New()
	v.Set(FlagAppDBMaxOpenFiles, "2048")
	appDBOptions = v
	require.Equal(t, 2048, appDBMaxOpenFiles())

	db, err := openAppDB(t.TempDir(), dbm.GoLevelDBBackend)
	require.NoError(t, err)
	require.NoError(t, db.Set([]byte("k"), []byte("v")))
	require.NoError(t, db.Close())
}

func TestCapOpenFiles(t *testing.T) {
	require.Equal(t, 16384, capOpenFiles(16384, 0))
	require.Equal(t, 16384, capOpenFiles(16384, 1<<20))
	require.Equal(t, 2048, capOpenFiles(16384, 8192))
	require.Equal(t, goleveldbDefaultOpenFiles, capOpenFiles(16384, 1024))
}

func TestServerCommandsKept(t *testing.T) {
	rootCmd := &cobra.Command{Use: "inferenced"}
	addServerCommands(rootCmd)
	for _, name := range []string{"start", "comet", "export", "version", "rollback", "module-hash-by-height"} {
		c, _, err := rootCmd.Find([]string{name})
		require.NoError(t, err, name)
		require.Equal(t, name, c.Name())
	}
}
