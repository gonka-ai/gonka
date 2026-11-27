package postgres

import (
	"errors"
	"time"
)

const (
	bootTimeout            = 30 * time.Second
	markerTable            = "gateway_database_marker"
	bootLockName           = "gateway_store_boot"
	nowExpression          = `to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS')`
	synchronousCommitParam = "synchronous_commit"
	synchronousCommitOn    = "on"
	fsyncSetting           = "fsync"
	settingOff             = "off"
	serializationFailure   = "40001"
	deadlockDetected       = "40P01"
	flagSet                = int64(1)
	flagClear              = int64(0)
	loopbackHostName       = "localhost"
	unixSocketHostPrefix   = "/"
)

// The errors Open returns for a database that is not the gateway's, a remote host without TLS and a server that does not flush commits.
var (
	ErrForeignDatabase     = errors.New("postgres database holds tables this gateway did not create; point the gateway at a dedicated, empty database")
	ErrPlaintextRemoteHost = errors.New("postgres connection to a remote host would not use TLS; set sslmode to require, verify-ca or verify-full")
	ErrDurabilityOff       = errors.New("postgres server does not flush commits to disk; a saved commitment would not survive a crash")
)
