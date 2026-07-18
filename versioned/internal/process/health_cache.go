package process

import (
	"context"
	"time"

	"versioned/internal/health"
)

const defaultHealthSampleInterval = time.Second

// StartHealthMonitor refreshes child lifecycle counters out of band. This
// keeps /healthz bounded even when a child admin listener is slow or missing.
func (m *Manager) StartHealthMonitor(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = defaultHealthSampleInterval
	}
	m.healthMonitorOnce.Do(func() {
		go func() {
			m.refreshHealthCache(ctx)
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					m.refreshHealthCache(ctx)
				}
			}
		}()
	})
}

func (m *Manager) refreshHealthCache(ctx context.Context) {
	entries := m.StatusWithInflight(ctx)
	m.healthCache.Store(cloneStatusEntries(entries))
}

func (m *Manager) CachedStatusWithInflight() []health.StatusEntry {
	entries, _ := m.healthCache.Load().([]health.StatusEntry)
	return cloneStatusEntries(entries)
}

func cloneStatusEntries(entries []health.StatusEntry) []health.StatusEntry {
	return append([]health.StatusEntry(nil), entries...)
}
