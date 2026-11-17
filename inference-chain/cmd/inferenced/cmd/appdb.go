package cmd

import (
	"path/filepath"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/spf13/cast"
)

const (
	// FlagAppDBMaxOpenFiles sets the goleveldb open-table cache of application.db
	// (app.toml top level or INFERENCED_APP_DB_MAX_OPEN_FILES).
	FlagAppDBMaxOpenFiles = "app-db-max-open-files"

	goleveldbDefaultOpenFiles = 500
	defaultAppDBMaxOpenFiles  = 16384
)

// appDBOptions is the start command's viper, set once its config is loaded (root PersistentPreRunE).
var appDBOptions dbm.Options

// openAppDB is the SDK's openDB with a goleveldb table cache sized for the state:
// at goleveldb's default of 500, IAVL reads on a multi-GB store keep reopening tables.
func openAppDB(rootDir string, backendType dbm.BackendType) (dbm.DB, error) {
	dataDir := filepath.Join(rootDir, "data")
	if backendType != dbm.GoLevelDBBackend {
		return dbm.NewDB("application", backendType, dataDir)
	}
	return dbm.NewDBwithOptions("application", backendType, dataDir, maxOpenFilesOptions{appDBMaxOpenFiles()})
}

func appDBMaxOpenFiles() int {
	if appDBOptions != nil {
		if n := cast.ToInt(appDBOptions.Get(FlagAppDBMaxOpenFiles)); n > 0 {
			return n
		}
	}
	return capOpenFiles(defaultAppDBMaxOpenFiles, fileLimit())
}

// capOpenFiles keeps the table cache within a quarter of the fd limit (0 = unknown), never below goleveldb's default.
func capOpenFiles(want int, limit uint64) int {
	if limit > 0 && limit/4 < uint64(want) {
		want = int(limit / 4)
	}
	return max(want, goleveldbDefaultOpenFiles)
}

type maxOpenFilesOptions struct{ files int }

func (o maxOpenFilesOptions) Get(key string) interface{} {
	if key == "maxopenfiles" {
		return o.files
	}
	return nil
}
