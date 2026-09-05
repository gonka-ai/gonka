//go:build devshard_testenv

package session

import "os"

func testenvOracleFromEnv() (delta int64, fabricate bool) {
	return parseOracleOverlay(os.Getenv(envTestenvOracleHeightDelta), os.Getenv(envTestenvOracleFabricateHash))
}
