package event_listener

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// errCorruptHeadMarker means the marker file exists and its JSON does not parse.
// An I/O error is not corrupt: boot retries the read instead of resetting.
var errCorruptHeadMarker = errors.New("host event head marker is corrupt")

// headMarker is the last height this process served for one writer.
// It lives beside the data directory so a rolled-back database is visible at boot.
type headMarker struct {
	Writer     string `json:"writer"`
	Generation uint64 `json:"generation"`
	Seq        uint64 `json:"seq"`
	Height     int64  `json:"height"`
}

func markerPath(dataDir string) string {
	return filepath.Join(dataDir, "host_events.head")
}

func writeHeadMarkerFile(path string, m headMarker) error {
	if path == "" {
		return errors.New("host event head marker path is empty")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "host_events.head.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	enc, err := json.Marshal(m)
	if err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(enc); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	ok = true
	dirf, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer dirf.Close()
	return dirf.Sync()
}

func readHeadMarker(path string) (headMarker, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return headMarker{}, false, nil
		}
		return headMarker{}, false, err
	}
	var m headMarker
	if err := json.Unmarshal(raw, &m); err != nil {
		return headMarker{}, false, fmt.Errorf("%w: %s", errCorruptHeadMarker, err.Error())
	}
	return m, true, nil
}
