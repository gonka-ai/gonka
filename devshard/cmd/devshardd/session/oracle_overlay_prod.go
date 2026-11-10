//go:build !devshard_testenv

package session

// testenvOracleFromEnv is a no-op in production: a stray
// DEVSHARD_TESTENV_ORACLE_* must not shift a real host's tip.
func testenvOracleFromEnv() (delta int64, fabricate bool) {
	return 0, false
}
