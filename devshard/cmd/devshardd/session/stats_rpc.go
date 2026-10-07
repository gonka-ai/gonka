package session

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"

	"devshard/transport"
)

const rpcStatsCacheTTL = 15 * time.Second

func (m *HostManager) handleStatsRPC(c echo.Context) error {
	body, err := m.statsRPCJSON(time.Now())
	if err != nil {
		return statsHTTPError(err)
	}
	return writeJSONMaybeGzip(c, body)
}

func (m *HostManager) statsRPCJSON(now time.Time) ([]byte, error) {
	openMinute := now.Unix() / 60
	m.statsMu.Lock()
	if m.statsRPCCache != nil && now.Sub(m.statsRPCCached) < rpcStatsCacheTTL && m.statsRPCCachedMinute == openMinute {
		body := m.statsRPCCache
		m.statsMu.Unlock()
		return body, nil
	}
	m.statsMu.Unlock()

	v, err, _ := m.sf.Do("stats/rpc/"+strconv.FormatInt(openMinute, 10), func() (any, error) {
		snap := m.buildRPCStats(now)
		return json.Marshal(snap)
	})
	if err != nil {
		return nil, err
	}
	body := v.([]byte)
	m.statsMu.Lock()
	m.statsRPCCache = body
	m.statsRPCCached = now
	m.statsRPCCachedMinute = openMinute
	m.statsMu.Unlock()
	return body, nil
}

func (m *HostManager) buildRPCStats(now time.Time) transport.RPCStatsSnapshot {
	var traffic *transport.RPCTraffic
	if auth := m.rpcAuth.Load(); auth != nil {
		traffic = auth.Traffic()
	}
	snap := traffic.Snapshot(now)
	snap.HostAddress = m.hostRPCAddress()
	snap.ProtocolVersion = m.boundVersion
	snap.BinaryVersion = m.binaryVersion
	snap.Host.Reconnects = transport.SnapshotPeerReconnects(now)
	if snap.Shards == nil {
		snap.Shards = []transport.RPCStatsShard{}
	}
	_, active, err := m.boundVersionActiveSessions()
	if err == nil {
		for _, sess := range active {
			transport.EnsureShard(&snap, sess.EscrowID, m.boundVersion)
		}
	}
	for i := range snap.Shards {
		if snap.Shards[i].ProtocolVersion == "" {
			snap.Shards[i].ProtocolVersion = m.boundVersion
		}
	}
	return snap
}

func writeJSONMaybeGzip(c echo.Context, payload []byte) error {
	c.Response().Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSONCharsetUTF8)
	if transport.AcceptsGzip(c.Request().Header) && len(payload) >= transport.MinGzipBodyBytes {
		gz, err := transport.GzipBestSpeed(payload)
		if err != nil {
			return err
		}
		c.Response().Header().Set(echo.HeaderContentEncoding, "gzip")
		return c.Blob(http.StatusOK, echo.MIMEApplicationJSONCharsetUTF8, gz)
	}
	return c.Blob(http.StatusOK, echo.MIMEApplicationJSONCharsetUTF8, payload)
}
