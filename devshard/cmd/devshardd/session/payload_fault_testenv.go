//go:build devshard_testenv

package session

import "os"

// payloadFaultBuildEnabled reports whether this binary can inject payload faults.
const payloadFaultBuildEnabled = true

func payloadFaultFromEnv() (status int, validatorAddr string) {
	return parsePayloadFault(os.Getenv(envTestenvPayloadHTTPStatus), os.Getenv(envTestenvPayloadFaultAddr))
}
