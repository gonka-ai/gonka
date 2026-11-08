package metrics

import "github.com/prometheus/client_golang/prometheus"

// CacheSource is the response cache's own account of itself; plain integers avoid an import cycle with api.
type CacheSource interface {
	CacheStats() (hits, misses, entries, bytes int64)
}

type CacheCollector struct {
	cache CacheSource

	hits    *prometheus.Desc
	misses  *prometheus.Desc
	entries *prometheus.Desc
	bytes   *prometheus.Desc
}

func NewCacheCollector(cache CacheSource) *CacheCollector {
	return &CacheCollector{
		cache:   cache,
		hits:    counterDesc("devshard_gateway_cache_hits_total", "Replies served from the response cache. A hit runs no race, so it appears in no attempt, nonce or escrow metric."),
		misses:  counterDesc("devshard_gateway_cache_misses_total", "Requests the response cache could not serve. Against hits this is the only measure of whether the cache earns its memory."),
		entries: gaugeDesc("devshard_gateway_cache_entries", "Replies the response cache currently holds."),
		bytes:   gaugeDesc("devshard_gateway_cache_bytes", "Bytes the response cache currently holds."),
	}
}

func (c *CacheCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.hits
	ch <- c.misses
	ch <- c.entries
	ch <- c.bytes
}

func (c *CacheCollector) Collect(ch chan<- prometheus.Metric) {
	if c.cache == nil {
		return
	}
	hits, misses, entries, bytes := c.cache.CacheStats()
	counter(ch, c.hits, float64(hits))
	counter(ch, c.misses, float64(misses))
	gauge(ch, c.entries, float64(entries))
	gauge(ch, c.bytes, float64(bytes))
}
