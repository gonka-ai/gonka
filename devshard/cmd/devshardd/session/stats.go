package session

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"common/logging"
	inferenceTypes "github.com/productscience/inference/x/inference/types"

	"devshard/observability"
	devshardserver "devshard/server"
	"devshard/storage"
	"devshard/types"
)

const statsCacheTTL = 60 * time.Second

type statsShardDetailCache struct {
	response *statsShardDetailResponse
	cached   time.Time
}

type statsShardsResponse struct {
	CurrentEpochID  uint64              `json:"current_epoch_id"`
	CachedAt        int64               `json:"cached_at"`
	CacheTTLSeconds int64               `json:"cache_ttl_seconds"`
	ActiveEscrows   []string            `json:"active_escrows"`
	Shards          []statsShardSummary `json:"shards"`
}

type statsShardSummary struct {
	EscrowID string `json:"escrow_id"`
	EpochID  uint64 `json:"epoch_id"`
}

type statsShardDetailResponse struct {
	EscrowID                    string                       `json:"escrow_id"`
	EpochID                     uint64                       `json:"epoch_id"`
	Nonce                       uint64                       `json:"nonce"`
	Version                     string                       `json:"version"` // versiond runtime bind (m.boundVersion)
	StateRootAndProtocolVersion string                       `json:"state_root_and_protocol_version"`
	CachedAt                    int64                        `json:"cached_at"`
	CacheTTLSeconds             int64                        `json:"cache_ttl_seconds"`
	HostStats                   map[uint32]statsHostStats    `json:"host_stats"`
	ValidationObservability     statsValidationObservability `json:"validation_observability"`
	Group                       []types.SlotAssignment       `json:"group"`
}

type statsHostStats struct {
	Missed               uint32 `json:"missed"`
	Invalid              uint32 `json:"invalid"`
	Cost                 uint64 `json:"cost"`
	RequiredValidations  uint32 `json:"required_validations"`
	CompletedValidations uint32 `json:"completed_validations"`
}

// statsValidationTotals aggregates per-slot observability rows; uint64 avoids wrap
// when summing many slots (per-slot counters remain uint32).
type statsValidationTotals struct {
	RequiredValidations  uint64 `json:"required_validations"`
	CompletedValidations uint64 `json:"completed_validations"`
}

// statsValidationObservability exposes validation counters persisted outside the
// state root (survives host restart; not used for settlement).
type statsValidationObservability struct {
	BySlot map[uint32]statsHostStats `json:"by_slot"`
	Totals statsValidationTotals     `json:"totals"`
}

func (m *HostManager) handleStatsShards(c echo.Context) error {
	resp, err := m.statsShards(time.Now())
	if err != nil {
		return statsHTTPError(err)
	}
	return c.JSON(http.StatusOK, resp)
}

func (m *HostManager) handleStatsShard(c echo.Context) error {
	escrowID := c.Param("escrow_id")
	if escrowID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "escrow_id required")
	}
	resp, err := m.statsShardDetail(escrowID, time.Now())
	if err != nil {
		recordStatsSessionResolutionFailure(c, escrowID, err)
		return statsHTTPError(err)
	}
	return c.JSON(http.StatusOK, resp)
}

func recordStatsSessionResolutionFailure(c echo.Context, escrowID string, err error) {
	status, reason := statsSessionResolutionStatus(err)
	observability.IncSessionResolution("stats_shard_detail", status, reason)
	observability.Log(c.Request().Context(), observability.LevelWarn,
		"devshard stats session resolution failed",
		observability.StageSessionResolved,
		observability.WhereRoutesSessionResolve,
		escrowID,
		reason,
		err,
	)
}

func statsSessionResolutionStatus(err error) (observability.MetricStatus, observability.Reason) {
	if errors.Is(err, storage.ErrSessionVersionConflict) {
		return observability.MetricStatusError, observability.ReasonVersionConflict
	}
	if errors.Is(err, storage.ErrSessionEpochConflict) {
		return observability.MetricStatusError, observability.ReasonEpochConflict
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "build group"):
		return observability.MetricStatusError, observability.ReasonBuildGroupErr
	case strings.Contains(msg, "get escrow"):
		return observability.MetricStatusError, observability.ReasonGetEscrowErr
	case strings.Contains(msg, "storage"):
		return observability.MetricStatusError, observability.ReasonStorageErr
	default:
		return observability.MetricStatusError, observability.ReasonSessionResolveErr
	}
}

func (m *HostManager) statsShards(now time.Time) (*statsShardsResponse, error) {
	m.statsMu.Lock()
	if m.statsShardsCache != nil && now.Sub(m.statsShardsCached) < statsCacheTTL {
		resp := m.statsShardsCache
		m.statsMu.Unlock()
		return resp, nil
	}
	m.statsMu.Unlock()

	currentEpochID, active, err := m.boundVersionActiveSessions()
	if err != nil {
		return nil, err
	}

	resp := &statsShardsResponse{
		CurrentEpochID:  currentEpochID,
		CachedAt:        now.Unix(),
		CacheTTLSeconds: int64(statsCacheTTL / time.Second),
		ActiveEscrows:   make([]string, 0, len(active)),
		Shards:          make([]statsShardSummary, 0, len(active)),
	}
	for _, sess := range active {
		resp.ActiveEscrows = append(resp.ActiveEscrows, sess.EscrowID)
		resp.Shards = append(resp.Shards, statsShardSummary{
			EscrowID: sess.EscrowID,
			EpochID:  sess.EpochID,
		})
	}

	m.statsMu.Lock()
	m.statsShardsCache = resp
	m.statsShardsCached = now
	m.statsMu.Unlock()
	return resp, nil
}

func (m *HostManager) statsShardDetail(escrowID string, now time.Time) (*statsShardDetailResponse, error) {
	m.statsMu.Lock()
	if cached, ok := m.statsDetailsCache[escrowID]; ok && now.Sub(cached.cached) < statsCacheTTL {
		resp := cached.response
		m.statsMu.Unlock()
		return resp, nil
	}
	m.statsMu.Unlock()

	sess, err := m.boundVersionActiveSession(escrowID)
	if err != nil {
		return nil, err
	}
	// Prefer recovering an already-persisted session over create-via-bridge.
	if err := m.TryLoadFromStorage(escrowID); err != nil {
		return nil, err
	}
	srv, err := m.SessionServer(escrowID)
	if err != nil {
		return nil, err
	}

	st := srv.Host().SnapshotState()

	resp := &statsShardDetailResponse{
		EscrowID:                    escrowID,
		EpochID:                     sess.EpochID,
		Nonce:                       st.LatestNonce,
		Version:                     m.boundVersion,
		StateRootAndProtocolVersion: st.StateRootAndProtocolVersion,
		CachedAt:                    now.Unix(),
		CacheTTLSeconds:             int64(statsCacheTTL / time.Second),
		HostStats:                   statsHostStatsFromState(st.HostStats),
		ValidationObservability:     validationObservabilityFromStore(m.store, escrowID),
		Group:                       append([]types.SlotAssignment(nil), st.Group...),
	}

	m.statsMu.Lock()
	m.statsDetailsCache[escrowID] = statsShardDetailCache{response: resp, cached: now}
	m.statsMu.Unlock()
	return resp, nil
}

func (m *HostManager) boundVersionActiveSession(escrowID string) (storage.ActiveSession, error) {
	currentEpochID, active, err := m.boundVersionActiveSessions()
	if err != nil {
		return storage.ActiveSession{}, err
	}
	for _, sess := range active {
		if sess.EscrowID == escrowID {
			return sess, nil
		}
	}
	m.logStatsSessionNotFound(escrowID, currentEpochID, active)
	return storage.ActiveSession{}, storage.ErrSessionNotFound
}

// logStatsSessionNotFound explains why detail stats 404'd: missing from storage
// (pruned / never created) or filtered by bound version. Rate-limited only by
// the caller's polling; keep fields compact for grep in CI dumps.
func (m *HostManager) logStatsSessionNotFound(escrowID string, currentEpochID uint64, filtered []storage.ActiveSession) {
	all, listErr := m.store.ListActiveSessions()
	filteredIDs := make([]string, 0, len(filtered))
	for _, sess := range filtered {
		filteredIDs = append(filteredIDs, sess.EscrowID)
	}

	var (
		foundInStore bool
		sessionEpoch uint64
		metaVersion  string
		versionMatch bool
		metaErr      error
	)
	if listErr == nil {
		for _, sess := range all {
			if sess.EscrowID != escrowID {
				continue
			}
			foundInStore = true
			sessionEpoch = sess.EpochID
			metaVersion, versionMatch, metaErr = m.sessionMatchesBoundVersion(escrowID)
			break
		}
	}

	reason := "absent_from_active_store"
	switch {
	case listErr != nil:
		reason = "list_active_failed"
	case !foundInStore:
		reason = "absent_from_active_store"
	case metaErr != nil:
		reason = "meta_unreadable"
	case !versionMatch:
		reason = "version_mismatch"
	default:
		reason = "filtered_unknown"
	}

	storeSummary := make([]string, 0, len(all))
	for _, sess := range all {
		storeSummary = append(storeSummary, fmt.Sprintf("%s@%d", sess.EscrowID, sess.EpochID))
	}

	logging.Warn("devshard stats session not in bound-version active set",
		inferenceTypes.System,
		"escrow_id", escrowID,
		"filter_reason", reason,
		"current_epoch_id", currentEpochID,
		"session_epoch_id", sessionEpoch,
		"found_in_store", foundInStore,
		"bound_version", m.boundVersion,
		"meta_version", metaVersion,
		"version_match", versionMatch,
		"meta_error", metaErr,
		"list_error", listErr,
		"filtered_escrows", strings.Join(filteredIDs, ","),
		"store_escrows", strings.Join(storeSummary, ","),
	)
}

// boundVersionActiveSessions returns non-pruned active sessions for this
// versiond bind. Epoch is recorded on each shard but not used as a filter:
// unsettled sessions remain queryable across epoch boundaries until prune.
// currentEpochID is still returned for list metadata / operators.
func (m *HostManager) boundVersionActiveSessions() (uint64, []storage.ActiveSession, error) {
	active, err := m.store.ListActiveSessions()
	if err != nil {
		return 0, nil, fmt.Errorf("list active sessions: %w", err)
	}

	currentEpochID := currentEpochIDFromStore(m.store)
	if currentEpochID == 0 {
		for _, sess := range active {
			if sess.EpochID > currentEpochID {
				currentEpochID = sess.EpochID
			}
		}
	}

	filtered := make([]storage.ActiveSession, 0, len(active))
	for _, sess := range active {
		_, matches, err := m.sessionMatchesBoundVersion(sess.EscrowID)
		if err != nil {
			logging.Debug("skipping devshard stats session with unreadable meta",
				inferenceTypes.System,
				"escrow_id", sess.EscrowID,
				"epoch_id", sess.EpochID,
				"error", err,
			)
			continue
		}
		if !matches {
			continue
		}
		filtered = append(filtered, sess)
	}
	slices.SortFunc(filtered, func(a, b storage.ActiveSession) int {
		return cmp.Compare(a.EscrowID, b.EscrowID)
	})
	return currentEpochID, filtered, nil
}

func (m *HostManager) sessionMatchesBoundVersion(escrowID string) (string, bool, error) {
	meta, err := m.store.GetSessionMeta(escrowID)
	if err != nil {
		return "", false, err
	}
	if meta.Version == "" || meta.Version == m.boundVersion {
		return meta.Version, true, nil
	}
	return meta.Version, false, nil
}

func statsHostStatsFromState(src map[uint32]*types.HostStats) map[uint32]statsHostStats {
	dst := make(map[uint32]statsHostStats, len(src))
	for slotID, stats := range src {
		if stats == nil {
			dst[slotID] = statsHostStats{}
			continue
		}
		dst[slotID] = statsHostStats{
			Missed:               stats.Missed,
			Invalid:              stats.Invalid,
			Cost:                 stats.Cost,
			RequiredValidations:  stats.RequiredValidations,
			CompletedValidations: stats.CompletedValidations,
		}
	}
	return dst
}

func validationObservabilityFromStore(store storage.Storage, escrowID string) statsValidationObservability {
	out := statsValidationObservability{
		BySlot: make(map[uint32]statsHostStats),
	}
	if store == nil {
		return out
	}
	rows, err := store.GetValidationObservability(escrowID)
	if err != nil {
		logging.Warn("validation observability read failed", inferenceTypes.System,
			"escrow_id", escrowID,
			"error", err,
		)
		return out
	}
	for _, row := range rows {
		out.BySlot[row.SlotID] = statsHostStats{
			RequiredValidations:  row.RequiredValidations,
			CompletedValidations: row.CompletedValidations,
		}
		out.Totals.RequiredValidations += uint64(row.RequiredValidations)
		out.Totals.CompletedValidations += uint64(row.CompletedValidations)
	}
	return out
}

func statsHTTPError(err error) error {
	if errors.Is(err, storage.ErrSessionNotFound) {
		return echo.NewHTTPError(http.StatusNotFound, "shard not found")
	}
	if errors.Is(err, storage.ErrSessionVersionConflict) || errors.Is(err, storage.ErrSessionEpochConflict) {
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	}
	if errors.Is(err, devshardserver.ErrInitializing) {
		return echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
	}
	return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
}

func currentEpochIDFromStore(store storage.Storage) uint64 {
	type currentEpochProvider interface {
		CurrentEpochID() uint64
	}
	if p, ok := store.(currentEpochProvider); ok {
		return p.CurrentEpochID()
	}
	return 0
}
