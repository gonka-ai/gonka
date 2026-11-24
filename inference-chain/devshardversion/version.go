// Package devshardversion contains protocol rules shared by the chain and runtime.
package devshardversion

import (
	"strconv"
	"strings"
)

// UsesAssignedCount enables exact per-slot inference accounting for numeric
// protocol tags v7 and later. It follows the runtime's leading major version
// convention (v7, 7.1, v7r2). Named and pre-v7 tags retain legacy accounting.
// The version is the escrow's frozen state-root tag, never a node-local flag.
func UsesAssignedCount(version string) bool {
	v := strings.TrimSpace(version)
	if strings.HasPrefix(v, "v") || strings.HasPrefix(v, "V") {
		v = v[1:]
	}
	i := 0
	for i < len(v) && v[i] >= '0' && v[i] <= '9' {
		i++
	}
	major, err := strconv.ParseUint(v[:i], 10, 32)
	return err == nil && major >= 7
}
