//go:build !devshard_testenv

package session

// payloadFaultBuildEnabled reports whether this binary can inject payload faults.
const payloadFaultBuildEnabled = false

// payloadFaultFromEnv ignores DEVSHARD_TESTENV_PAYLOAD_* in production builds:
// honouring it would let an environment mistake fail every payload GET before
// authentication, which other validators now punish with a false vote.
func payloadFaultFromEnv() (status int, validatorAddr string) {
	return 0, ""
}
