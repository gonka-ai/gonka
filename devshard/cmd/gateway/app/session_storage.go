package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func removeDevshardStorage(storagePath, baseStorageDir string) error {
	// The escrow id in storagePath came from a client, so os.RemoveAll runs only on a directory strictly inside the base.
	storagePath = filepath.Clean(storagePath)
	baseStorageDir = filepath.Clean(baseStorageDir)
	if !strings.HasPrefix(storagePath, baseStorageDir+string(os.PathSeparator)) && storagePath != baseStorageDir {
		return fmt.Errorf("refusing to delete storage outside base dir: %s", storagePath)
	}
	if storagePath == baseStorageDir {
		return fmt.Errorf("refusing to delete base storage dir: %s", storagePath)
	}
	return os.RemoveAll(storagePath)
}
