package sqlite

import "errors"

const (
	gatewayDatabaseFileName = "gateway.db"
	storageDirectoryMode    = 0o755
	connectionPragmas       = "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	retryPrimaryCodeMask    = 0xFF
	checkpointNotBusy       = 0
)

// ErrLegacyDatabase reports a gateway.db this gateway did not create. See ../README.md, "The devshardctl-database guard".
var (
	ErrLegacyDatabase = errors.New("storage dir holds a gateway.db this gateway did not create; point the gateway at an empty storage dir")

	errCheckpointBusy = errors.New("checkpoint could not move the whole log into the main database file")

	legacyOnlyTables = []string{"gateway_settings", "gateway_devshards", "gateway_suspicious_hosts", "participant_throttle_state"}
)
