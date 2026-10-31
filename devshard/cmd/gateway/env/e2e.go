package env

import (
	"time"

	"devshard/cmd/gateway/internal/logkey"
	"devshard/internal/e2econfig"
	"devshard/logging"
)

// E2E is what a declared end-to-end stand may shorten. See docs/rules.md, "What a test stand may reach".
type E2E struct {
	StreamingHardTimeout time.Duration
	SessionTimeouts      e2econfig.SessionTimeoutOverrides
}

// A refused value is dropped rather than returned: it is inert by design, and must not stop a boot.
func LoadE2E() E2E {
	loaded := E2E{}
	streamingHardTimeout, err := e2econfig.DurationMillisFromEnv(e2econfig.StreamingHardTimeoutMillisEnv)
	if err != nil {
		logging.Warn("end-to-end override ignored", logkey.Subsystem, "env",
			logkey.Recorded, e2econfig.StreamingHardTimeoutMillisEnv, logkey.Error, err)
	} else {
		loaded.StreamingHardTimeout = streamingHardTimeout
	}

	sessionTimeouts, err := e2econfig.SessionTimeoutOverridesFromEnv()
	if err != nil {
		logging.Warn("end-to-end override ignored", logkey.Subsystem, "env",
			logkey.Recorded, e2econfig.ExecutionTimeoutSecondsEnv, logkey.Error, err)
		return loaded
	}
	loaded.SessionTimeouts = sessionTimeouts
	return loaded
}
