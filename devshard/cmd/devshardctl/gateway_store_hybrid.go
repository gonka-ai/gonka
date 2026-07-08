package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	gatewayPGConnectTimeout = 2 * time.Second
	// defaultGatewayPGRetryInterval is how long we stay on the SQLite fallback
	// before probing Postgres again after giving up. Kept short (relative to the
	// payload store) because gateway management state is low write volume, so a
	// snappier recovery + journal drain outweighs the occasional reconnect probe.
	defaultGatewayPGRetryInterval = 15 * time.Second
	// defaultGatewayPGWriteRetryBudget bounds the in-request retry loop that lets
	// a brief Postgres blip (reset/failover) recover without dropping to SQLite.
	defaultGatewayPGWriteRetryBudget = 2 * time.Second
)

// gatewayPGWriteRetryBaseBackoff is the first inter-attempt sleep; it doubles
// each retry, bounded by the remaining budget. A var so tests can shrink it.
var gatewayPGWriteRetryBaseBackoff = 500 * time.Millisecond

// HybridGatewayStore uses Postgres as primary storage with SQLite fallback.
// Writes try Postgres first (with lazy reconnect), then SQLite on failure.
// Reads try Postgres first without reconnect delay; on error or empty state
// they also check SQLite.
type HybridGatewayStore struct {
	pg                 *PostgresGatewayStore
	sqlite             *SQLiteGatewayStore
	mu                 sync.Mutex
	lastRetry          time.Time
	retryInterval      time.Duration
	connectTimeout     time.Duration
	writeRetryBudget      time.Duration
	sqliteFallbackEnabled bool
	connectPG             func(context.Context) (*PostgresGatewayStore, error)
}

func NewHybridGatewayStore(pg *PostgresGatewayStore, sqlite *SQLiteGatewayStore, retryInterval, connectTimeout time.Duration) *HybridGatewayStore {
	if retryInterval <= 0 {
		retryInterval = defaultGatewayPGRetryInterval
	}
	if connectTimeout <= 0 {
		connectTimeout = gatewayPGConnectTimeout
	}
	return &HybridGatewayStore{
		pg:                    pg,
		sqlite:                sqlite,
		retryInterval:         retryInterval,
		connectTimeout:        connectTimeout,
		writeRetryBudget:      gatewayPGWriteRetryBudget(),
		sqliteFallbackEnabled: gatewayPGToSQLiteFallback(),
		connectPG:             NewPostgresGatewayStore,
	}
}

// gatewayPGToSQLiteFallback reads PG_TO_SQLITE_FALLBACK (default true). When
// false, Postgres failures return errors instead of falling back to SQLite; the
// sync journal is only used while fallback is enabled.
func gatewayPGToSQLiteFallback() bool {
	raw := strings.TrimSpace(os.Getenv("PG_TO_SQLITE_FALLBACK"))
	if raw == "" {
		return true
	}
	switch strings.ToLower(raw) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func (h *HybridGatewayStore) sqliteFallbackAllowed() bool {
	return h.sqliteFallbackEnabled && h.sqlite != nil
}

// gatewayPGWriteRetryBudget reads PG_WRITE_RETRY_BUDGET (a Go duration). A value
// of 0 disables in-request retry (single attempt then fallback); an invalid or
// unset value uses the default.
func gatewayPGWriteRetryBudget() time.Duration {
	v := strings.TrimSpace(os.Getenv("PG_WRITE_RETRY_BUDGET"))
	if v == "" {
		return defaultGatewayPGWriteRetryBudget
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return defaultGatewayPGWriteRetryBudget
	}
	return d
}

func (h *HybridGatewayStore) shouldAttemptConnect() (bool, *PostgresGatewayStore) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.pg != nil {
		return false, h.pg
	}
	if time.Since(h.lastRetry) < h.retryInterval {
		return false, nil
	}
	h.lastRetry = time.Now()
	return true, nil
}

func (h *HybridGatewayStore) getOrConnectPg(ctx context.Context) *PostgresGatewayStore {
	shouldAttempt, pg := h.shouldAttemptConnect()
	if !shouldAttempt {
		return pg
	}

	connectCtx, cancel := context.WithTimeout(ctx, h.connectTimeout)
	defer cancel()

	newPg, err := h.connectPG(connectCtx)
	if err != nil {
		return nil
	}

	if err := h.reconcileAndPublishPg(ctx, newPg); err != nil {
		log.Printf("gateway hybrid: sync journal drain failed, staying on sqlite fallback: %v", err)
		_ = newPg.Close()
		return nil
	}
	return h.currentPg()
}

// ReconcileSyncJournal drains pending SQLite outage writes into Postgres and
// publishes the connection as primary. Used by the factory at startup.
func (h *HybridGatewayStore) ReconcileSyncJournal(ctx context.Context, pg *PostgresGatewayStore) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.reconcileAndPublishPgLocked(ctx, pg)
}

func (h *HybridGatewayStore) reconcileAndPublishPg(ctx context.Context, pg *PostgresGatewayStore) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.reconcileAndPublishPgLocked(ctx, pg)
}

func (h *HybridGatewayStore) reconcileAndPublishPgLocked(ctx context.Context, pg *PostgresGatewayStore) error {
	if pg == nil {
		return fmt.Errorf("postgres store unavailable")
	}
	if !h.sqliteFallbackEnabled || h.sqlite == nil {
		h.pg = pg
		log.Printf("gateway store: postgres connection established")
		return nil
	}
	for {
		empty, err := drainGatewaySyncJournal(ctx, h.sqlite, pg)
		if err != nil {
			return err
		}
		if empty {
			h.pg = pg
			log.Printf("gateway store: postgres connection established")
			return nil
		}
	}
}

func (h *HybridGatewayStore) write(
	ctx context.Context,
	pgFn func(*PostgresGatewayStore) error,
	sqliteFn func(*SQLiteGatewayStore) error,
	journal []gatewaySyncEntry,
	sqliteTxFn func(*sql.Tx) error,
) error {
	if pg := h.getOrConnectPg(ctx); pg != nil {
		if err := h.attemptPGWrite(ctx, pg, pgFn); err == nil {
			return nil
		} else {
			log.Printf("gateway hybrid: postgres write failed after retries: %v", err)
			// Drop the cached connection so subsequent writes during the outage
			// get journaled, and the next reconnect replays the journal before
			// republishing Postgres as primary. Without this, pgxpool would
			// transparently reconnect and serve PG writes again while stale journal
			// entries linger, which a later restart-time drain would replay on top
			// of newer PG rows.
			h.dropPg(pg)
			if !h.sqliteFallbackEnabled {
				return err
			}
		}
	} else if !h.sqliteFallbackEnabled {
		return errGatewayStoreUnavailable
	}
	if h.sqlite == nil {
		return errGatewayStoreUnavailable
	}
	if h.sqliteFallbackEnabled && len(journal) > 0 && sqliteTxFn != nil {
		return h.sqlite.writeWithSyncJournal(journal, sqliteTxFn)
	}
	return sqliteFn(h.sqlite)
}

// attemptPGWrite runs pgFn, retrying transient failures within writeRetryBudget
// so a brief Postgres blip (connection reset, failover) recovers on the next
// attempt — pgxpool hands out a fresh connection — instead of dropping to
// SQLite for a whole retry interval. Only the first write after Postgres truly
// goes down pays the full budget; once dropPg clears the cached connection the
// remaining outage writes skip Postgres entirely.
func (h *HybridGatewayStore) attemptPGWrite(ctx context.Context, pg *PostgresGatewayStore, pgFn func(*PostgresGatewayStore) error) error {
	deadline := time.Now().Add(h.writeRetryBudget)
	backoff := gatewayPGWriteRetryBaseBackoff
	for {
		err := pgFn(pg)
		if err == nil {
			return nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return err
		}
		wait := backoff
		if wait > remaining {
			wait = remaining
		}
		log.Printf("gateway hybrid: postgres write failed, retrying in %s: %v", wait, err)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return err
		}
		backoff *= 2
	}
}

func (h *HybridGatewayStore) currentPg() *PostgresGatewayStore {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pg
}

// dropPg clears the cached primary connection if it still points at failed, so
// subsequent operations route to the SQLite fallback (and journal) until a
// reconnect re-drains the journal. It is a no-op if another goroutine already
// swapped the connection, which keeps the pool close idempotent.
func (h *HybridGatewayStore) dropPg(failed *PostgresGatewayStore) {
	h.mu.Lock()
	shouldClose := h.pg != nil && h.pg == failed
	if shouldClose {
		h.pg = nil
	}
	h.mu.Unlock()
	if shouldClose {
		_ = failed.Close()
	}
}

func (h *HybridGatewayStore) loadState(ctx context.Context) (GatewayState, bool, error) {
	if pg := h.currentPg(); pg != nil {
		state, has, err := pg.LoadState()
		if err == nil && has {
			return state, true, nil
		}
		if err != nil {
			log.Printf("gateway hybrid: postgres load state failed, checking sqlite: %v", err)
			if !h.sqliteFallbackEnabled {
				return GatewayState{}, false, err
			}
		}
		if h.sqliteFallbackAllowed() {
			sqliteState, sqliteHas, sqliteErr := h.sqlite.LoadState()
			if sqliteErr == nil && sqliteHas {
				return sqliteState, true, nil
			}
			if err != nil {
				return GatewayState{}, false, err
			}
			return sqliteState, sqliteHas, sqliteErr
		}
		if err != nil {
			return GatewayState{}, false, err
		}
		return state, has, nil
	}

	if h.sqliteFallbackAllowed() {
		state, has, err := h.sqlite.LoadState()
		if err == nil && has {
			return state, true, nil
		}
		if pg := h.getOrConnectPg(ctx); pg != nil {
			return pg.LoadState()
		}
		return state, has, err
	}

	if pg := h.getOrConnectPg(ctx); pg != nil {
		return pg.LoadState()
	}
	return GatewayState{}, false, errGatewayStoreUnavailable
}

func (h *HybridGatewayStore) Close() error {
	var pgErr error
	if h.pg != nil {
		pgErr = h.pg.Close()
	}
	var sqliteErr error
	if h.sqlite != nil {
		sqliteErr = h.sqlite.Close()
	}
	if pgErr != nil {
		return pgErr
	}
	return sqliteErr
}

func (h *HybridGatewayStore) LoadState() (GatewayState, bool, error) {
	return h.loadState(context.Background())
}

func (h *HybridGatewayStore) Initialize(settings GatewaySettings, devshards []GatewayDevshardState) error {
	journal := []gatewaySyncEntry{{tableName: gatewayTableSettings, rowKey: gatewaySettingsRowKey, op: gatewaySyncOpUpsert}}
	for _, d := range devshards {
		journal = append(journal, gatewaySyncEntry{tableName: gatewayTableDevshards, rowKey: strings.TrimSpace(d.ID), op: gatewaySyncOpUpsert})
	}
	return h.write(context.Background(),
		func(pg *PostgresGatewayStore) error { return pg.Initialize(settings, devshards) },
		func(sqlite *SQLiteGatewayStore) error { return sqlite.Initialize(settings, devshards) },
		journal,
		func(tx *sql.Tx) error { return h.sqlite.initializeFallbackTx(tx, settings, devshards) },
	)
}

func (h *HybridGatewayStore) UpdateSettings(settings GatewaySettings) error {
	return h.write(context.Background(),
		func(pg *PostgresGatewayStore) error { return pg.UpdateSettings(settings) },
		func(sqlite *SQLiteGatewayStore) error { return sqlite.UpdateSettings(settings) },
		[]gatewaySyncEntry{{tableName: gatewayTableSettings, rowKey: gatewaySettingsRowKey, op: gatewaySyncOpUpsert}},
		func(tx *sql.Tx) error { return h.sqlite.updateSettingsTx(tx, settings) },
	)
}

func (h *HybridGatewayStore) SaveRotationStatus(status GatewayRotationStatus) error {
	key := gatewayRotationStatusRowKey(status.ModelID, status.Stage, status.Epoch)
	return h.write(context.Background(),
		func(pg *PostgresGatewayStore) error { return pg.SaveRotationStatus(status) },
		func(sqlite *SQLiteGatewayStore) error { return sqlite.SaveRotationStatus(status) },
		[]gatewaySyncEntry{{tableName: gatewayTableRotationStatus, rowKey: key, op: gatewaySyncOpUpsert}},
		func(tx *sql.Tx) error { return h.sqlite.saveRotationStatusTx(tx, status) },
	)
}

func (h *HybridGatewayStore) LoadRotationStatuses(limit int) ([]GatewayRotationStatus, error) {
	if pg := h.currentPg(); pg != nil {
		statuses, err := pg.LoadRotationStatuses(limit)
		if err == nil && len(statuses) > 0 {
			return statuses, nil
		}
		if err != nil {
			log.Printf("gateway hybrid: postgres load rotation statuses failed, checking sqlite: %v", err)
			if !h.sqliteFallbackEnabled {
				return nil, err
			}
		}
		if h.sqliteFallbackAllowed() {
			sqliteStatuses, sqliteErr := h.sqlite.LoadRotationStatuses(limit)
			if sqliteErr == nil && len(sqliteStatuses) > 0 {
				return sqliteStatuses, nil
			}
			if err != nil {
				return nil, err
			}
			return sqliteStatuses, sqliteErr
		}
		if err != nil {
			return nil, err
		}
		return statuses, nil
	}

	if h.sqliteFallbackAllowed() {
		statuses, err := h.sqlite.LoadRotationStatuses(limit)
		if err == nil && len(statuses) > 0 {
			return statuses, nil
		}
		if pg := h.getOrConnectPg(context.Background()); pg != nil {
			return pg.LoadRotationStatuses(limit)
		}
		return statuses, err
	}

	if pg := h.getOrConnectPg(context.Background()); pg != nil {
		return pg.LoadRotationStatuses(limit)
	}
	return nil, errGatewayStoreUnavailable
}

func (h *HybridGatewayStore) SaveCommitment(c GatewayEscrowCommitment) error {
	txHash := strings.TrimSpace(c.TxHash)
	return h.write(context.Background(),
		func(pg *PostgresGatewayStore) error { return pg.SaveCommitment(c) },
		func(sqlite *SQLiteGatewayStore) error { return sqlite.SaveCommitment(c) },
		[]gatewaySyncEntry{{tableName: gatewayTableCommitments, rowKey: txHash, op: gatewaySyncOpUpsert}},
		func(tx *sql.Tx) error { return h.sqlite.saveCommitmentTx(tx, c) },
	)
}

func (h *HybridGatewayStore) LoadCommitments() ([]GatewayEscrowCommitment, error) {
	if pg := h.currentPg(); pg != nil {
		commitments, err := pg.LoadCommitments()
		if err == nil && len(commitments) > 0 {
			return commitments, nil
		}
		if err != nil {
			log.Printf("gateway hybrid: postgres load commitments failed, checking sqlite: %v", err)
			if !h.sqliteFallbackEnabled {
				return nil, err
			}
		}
		if h.sqliteFallbackAllowed() {
			sqliteCommitments, sqliteErr := h.sqlite.LoadCommitments()
			if sqliteErr == nil && len(sqliteCommitments) > 0 {
				return sqliteCommitments, nil
			}
			if err != nil {
				return nil, err
			}
			return sqliteCommitments, sqliteErr
		}
		if err != nil {
			return nil, err
		}
		return commitments, nil
	}

	if h.sqliteFallbackAllowed() {
		commitments, err := h.sqlite.LoadCommitments()
		if err == nil && len(commitments) > 0 {
			return commitments, nil
		}
		if pg := h.getOrConnectPg(context.Background()); pg != nil {
			return pg.LoadCommitments()
		}
		return commitments, err
	}

	if pg := h.getOrConnectPg(context.Background()); pg != nil {
		return pg.LoadCommitments()
	}
	return nil, errGatewayStoreUnavailable
}

func (h *HybridGatewayStore) DeleteCommitment(txHash string) error {
	txHash = strings.TrimSpace(txHash)
	return h.write(context.Background(),
		func(pg *PostgresGatewayStore) error { return pg.DeleteCommitment(txHash) },
		func(sqlite *SQLiteGatewayStore) error { return sqlite.DeleteCommitment(txHash) },
		[]gatewaySyncEntry{{tableName: gatewayTableCommitments, rowKey: txHash, op: gatewaySyncOpDelete}},
		func(tx *sql.Tx) error { return h.sqlite.deleteCommitmentTx(tx, txHash) },
	)
}

func (h *HybridGatewayStore) LoadSuspiciousHosts() ([]GatewaySuspiciousHost, error) {
	if pg := h.currentPg(); pg != nil {
		hosts, err := pg.LoadSuspiciousHosts()
		if err == nil && len(hosts) > 0 {
			return hosts, nil
		}
		if err != nil {
			log.Printf("gateway hybrid: postgres load suspicious hosts failed, checking sqlite: %v", err)
			if !h.sqliteFallbackEnabled {
				return nil, err
			}
		}
		if h.sqliteFallbackAllowed() {
			sqliteHosts, sqliteErr := h.sqlite.LoadSuspiciousHosts()
			if sqliteErr == nil && len(sqliteHosts) > 0 {
				return sqliteHosts, nil
			}
			if err != nil {
				return nil, err
			}
			return sqliteHosts, sqliteErr
		}
		if err != nil {
			return nil, err
		}
		return hosts, nil
	}

	if h.sqliteFallbackAllowed() {
		hosts, err := h.sqlite.LoadSuspiciousHosts()
		if err == nil && len(hosts) > 0 {
			return hosts, nil
		}
		if pg := h.getOrConnectPg(context.Background()); pg != nil {
			return pg.LoadSuspiciousHosts()
		}
		return hosts, err
	}

	if pg := h.getOrConnectPg(context.Background()); pg != nil {
		return pg.LoadSuspiciousHosts()
	}
	return nil, errGatewayStoreUnavailable
}

func (h *HybridGatewayStore) UpsertSuspiciousHosts(participantKeys []string, note string) ([]GatewaySuspiciousHost, error) {
	var result []GatewaySuspiciousHost
	keys := normalizeParticipantKeys(participantKeys)
	journal := make([]gatewaySyncEntry, 0, len(keys))
	for _, key := range keys {
		journal = append(journal, gatewaySyncEntry{tableName: gatewayTableSuspiciousHosts, rowKey: key, op: gatewaySyncOpUpsert})
	}
	err := h.write(context.Background(),
		func(pg *PostgresGatewayStore) error {
			var err error
			result, err = pg.UpsertSuspiciousHosts(participantKeys, note)
			return err
		},
		func(sqlite *SQLiteGatewayStore) error {
			var err error
			result, err = sqlite.UpsertSuspiciousHosts(participantKeys, note)
			return err
		},
		journal,
		func(tx *sql.Tx) error {
			if err := h.sqlite.upsertSuspiciousHostsTx(tx, participantKeys, note); err != nil {
				return err
			}
			var loadErr error
			result, loadErr = h.sqlite.loadSuspiciousHostsTx(tx)
			return loadErr
		},
	)
	return result, err
}

func (h *HybridGatewayStore) DeleteSuspiciousHosts(participantKeys []string) ([]GatewaySuspiciousHost, error) {
	var result []GatewaySuspiciousHost
	keys := normalizeParticipantKeys(participantKeys)
	journal := make([]gatewaySyncEntry, 0, len(keys))
	for _, key := range keys {
		journal = append(journal, gatewaySyncEntry{tableName: gatewayTableSuspiciousHosts, rowKey: key, op: gatewaySyncOpDelete})
	}
	err := h.write(context.Background(),
		func(pg *PostgresGatewayStore) error {
			var err error
			result, err = pg.DeleteSuspiciousHosts(participantKeys)
			return err
		},
		func(sqlite *SQLiteGatewayStore) error {
			var err error
			result, err = sqlite.DeleteSuspiciousHosts(participantKeys)
			return err
		},
		journal,
		func(tx *sql.Tx) error {
			if err := h.sqlite.deleteSuspiciousHostsTx(tx, participantKeys); err != nil {
				return err
			}
			var loadErr error
			result, loadErr = h.sqlite.loadSuspiciousHostsTx(tx)
			return loadErr
		},
	)
	return result, err
}

func (h *HybridGatewayStore) UpsertDevshard(devshard GatewayDevshardState) error {
	id := strings.TrimSpace(devshard.ID)
	return h.write(context.Background(),
		func(pg *PostgresGatewayStore) error { return pg.UpsertDevshard(devshard) },
		func(sqlite *SQLiteGatewayStore) error { return sqlite.UpsertDevshard(devshard) },
		[]gatewaySyncEntry{{tableName: gatewayTableDevshards, rowKey: id, op: gatewaySyncOpUpsert}},
		func(tx *sql.Tx) error { return h.sqlite.upsertDevshardTx(tx, devshard, time.Now().UTC().Format(time.RFC3339Nano)) },
	)
}

func (h *HybridGatewayStore) GetDevshard(id string) (GatewayDevshardState, bool, error) {
	if pg := h.currentPg(); pg != nil {
		devshard, ok, err := pg.GetDevshard(id)
		if err == nil && ok {
			return devshard, true, nil
		}
		if err != nil {
			log.Printf("gateway hybrid: postgres get devshard failed, checking sqlite: %v", err)
		}
		if h.sqlite != nil {
			sqliteDevshard, sqliteOk, sqliteErr := h.sqlite.GetDevshard(id)
			if sqliteErr == nil && sqliteOk {
				return sqliteDevshard, true, nil
			}
			if err != nil {
				return GatewayDevshardState{}, false, err
			}
			return sqliteDevshard, sqliteOk, sqliteErr
		}
		if err != nil {
			return GatewayDevshardState{}, false, err
		}
		return devshard, ok, nil
	}

	if h.sqlite != nil {
		devshard, ok, err := h.sqlite.GetDevshard(id)
		if err == nil && ok {
			return devshard, true, nil
		}
		if pg := h.getOrConnectPg(context.Background()); pg != nil {
			return pg.GetDevshard(id)
		}
		return devshard, ok, err
	}

	if pg := h.getOrConnectPg(context.Background()); pg != nil {
		return pg.GetDevshard(id)
	}
	return GatewayDevshardState{}, false, errGatewayStoreUnavailable
}

func (h *HybridGatewayStore) SetDevshardActive(id string, active bool) error {
	id = strings.TrimSpace(id)
	return h.write(context.Background(),
		func(pg *PostgresGatewayStore) error { return pg.SetDevshardActive(id, active) },
		func(sqlite *SQLiteGatewayStore) error { return sqlite.SetDevshardActive(id, active) },
		[]gatewaySyncEntry{{tableName: gatewayTableDevshards, rowKey: id, op: gatewaySyncOpUpsert}},
		func(tx *sql.Tx) error { return h.sqlite.setDevshardActiveTx(tx, id, active) },
	)
}

func (h *HybridGatewayStore) SetDevshardSettlementPending(id string, pending bool) error {
	id = strings.TrimSpace(id)
	return h.write(context.Background(),
		func(pg *PostgresGatewayStore) error { return pg.SetDevshardSettlementPending(id, pending) },
		func(sqlite *SQLiteGatewayStore) error { return sqlite.SetDevshardSettlementPending(id, pending) },
		[]gatewaySyncEntry{{tableName: gatewayTableDevshards, rowKey: id, op: gatewaySyncOpUpsert}},
		func(tx *sql.Tx) error { return h.sqlite.setDevshardSettlementPendingTx(tx, id, pending) },
	)
}

func (h *HybridGatewayStore) DeleteDevshard(id string) error {
	id = strings.TrimSpace(id)
	return h.write(context.Background(),
		func(pg *PostgresGatewayStore) error { return pg.DeleteDevshard(id) },
		func(sqlite *SQLiteGatewayStore) error { return sqlite.DeleteDevshard(id) },
		[]gatewaySyncEntry{{tableName: gatewayTableDevshards, rowKey: id, op: gatewaySyncOpDelete}},
		func(tx *sql.Tx) error { return h.sqlite.deleteDevshardTx(tx, id) },
	)
}

func (h *HybridGatewayStore) SaveParticipantThrottle(key string, modelIDs []string, tokens float64, lastRefillAt time.Time, status int, quarantineUntil time.Time, failureStrikes int) error {
	return h.write(context.Background(),
		func(pg *PostgresGatewayStore) error {
			return pg.SaveParticipantThrottle(key, modelIDs, tokens, lastRefillAt, status, quarantineUntil, failureStrikes)
		},
		func(sqlite *SQLiteGatewayStore) error {
			return sqlite.SaveParticipantThrottle(key, modelIDs, tokens, lastRefillAt, status, quarantineUntil, failureStrikes)
		},
		[]gatewaySyncEntry{{tableName: gatewayTableThrottle, rowKey: key, op: gatewaySyncOpUpsert}},
		func(tx *sql.Tx) error {
			return h.sqlite.saveParticipantThrottleTx(tx, key, modelIDs, tokens, lastRefillAt, status, quarantineUntil, failureStrikes)
		},
	)
}

func (h *HybridGatewayStore) DeleteParticipantThrottle(key string) error {
	return h.write(context.Background(),
		func(pg *PostgresGatewayStore) error { return pg.DeleteParticipantThrottle(key) },
		func(sqlite *SQLiteGatewayStore) error { return sqlite.DeleteParticipantThrottle(key) },
		[]gatewaySyncEntry{{tableName: gatewayTableThrottle, rowKey: key, op: gatewaySyncOpDelete}},
		func(tx *sql.Tx) error { return h.sqlite.deleteParticipantThrottleTx(tx, key) },
	)
}

func (h *HybridGatewayStore) LoadParticipantThrottles() ([]ParticipantThrottleRow, error) {
	if pg := h.currentPg(); pg != nil {
		rows, err := pg.LoadParticipantThrottles()
		if err == nil && len(rows) > 0 {
			return rows, nil
		}
		if err != nil {
			log.Printf("gateway hybrid: postgres load participant throttles failed, checking sqlite: %v", err)
			if !h.sqliteFallbackEnabled {
				return nil, err
			}
		}
		if h.sqliteFallbackAllowed() {
			sqliteRows, sqliteErr := h.sqlite.LoadParticipantThrottles()
			if sqliteErr == nil && len(sqliteRows) > 0 {
				return sqliteRows, nil
			}
			if err != nil {
				return nil, err
			}
			return sqliteRows, sqliteErr
		}
		if err != nil {
			return nil, err
		}
		return rows, nil
	}

	if h.sqliteFallbackAllowed() {
		rows, err := h.sqlite.LoadParticipantThrottles()
		if err == nil && len(rows) > 0 {
			return rows, nil
		}
		if pg := h.getOrConnectPg(context.Background()); pg != nil {
			return pg.LoadParticipantThrottles()
		}
		return rows, err
	}

	if pg := h.getOrConnectPg(context.Background()); pg != nil {
		return pg.LoadParticipantThrottles()
	}
	return nil, errGatewayStoreUnavailable
}

var (
	errGatewayStoreUnavailable = fmt.Errorf("gateway store unavailable")
)

var _ GatewayStore = (*HybridGatewayStore)(nil)
