// Package profiling configures optional runtime contention profiling.
package profiling

import (
	"log"
	"os"
	"runtime"
	"strconv"
	"strings"
)

const enableContentionProfilesEnv = "DEVSHARD_ENABLE_CONTENTION_PROFILES"

// ConfigureContentionProfilesFromEnv enables the runtime samplers used by the
// load-test harness. Sampling is opt-in because mutex and block profiling add
// measurable overhead to normal performance runs.
func ConfigureContentionProfilesFromEnv() bool {
	raw := strings.TrimSpace(os.Getenv(enableContentionProfilesEnv))
	if raw == "" {
		return false
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		log.Printf("contention profiles disabled: invalid %s=%q", enableContentionProfilesEnv, raw)
		return false
	}
	if !enabled {
		return false
	}

	// Keep the default sampling light enough for diagnostic runs while still
	// exposing lock and blocking hotspots in pprof.
	runtime.SetMutexProfileFraction(5)
	runtime.SetBlockProfileRate(1_000_000)
	log.Printf("contention profiles enabled: mutex_fraction=5 block_rate_ns=1000000")
	return true
}
