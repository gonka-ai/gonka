package e2econfig

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"devshard/types"
)

const (
	RefusalTimeoutSecondsEnv   = "DEVSHARD_E2E_REFUSAL_TIMEOUT_SECONDS"
	ExecutionTimeoutSecondsEnv = "DEVSHARD_E2E_EXECUTION_TIMEOUT_SECONDS"
)

type SessionTimeoutOverrides struct {
	RefusalTimeoutSeconds   *int64
	ExecutionTimeoutSeconds *int64
}

func SessionTimeoutOverridesFromEnv() (SessionTimeoutOverrides, error) {
	refusalTimeout, ok, err := sessionTimeoutSecondsEnv(RefusalTimeoutSecondsEnv)
	if err != nil {
		return SessionTimeoutOverrides{}, err
	}
	overrides := SessionTimeoutOverrides{}
	if ok {
		overrides.RefusalTimeoutSeconds = &refusalTimeout
	}

	executionTimeout, ok, err := sessionTimeoutSecondsEnv(ExecutionTimeoutSecondsEnv)
	if err != nil {
		return SessionTimeoutOverrides{}, err
	}
	if ok {
		overrides.ExecutionTimeoutSeconds = &executionTimeout
	}

	return overrides, nil
}

func (o SessionTimeoutOverrides) Apply(config types.SessionConfig) types.SessionConfig {
	if o.RefusalTimeoutSeconds != nil {
		config.RefusalTimeout = *o.RefusalTimeoutSeconds
	}
	if o.ExecutionTimeoutSeconds != nil {
		config.ExecutionTimeout = *o.ExecutionTimeoutSeconds
	}
	return config
}

func sessionTimeoutSecondsEnv(name string) (int64, bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, false, nil
	}
	if os.Getenv("DEVSHARD_E2E") != "1" {
		return 0, false, fmt.Errorf("%s is only supported when DEVSHARD_E2E=1", name)
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("invalid %s: %w", name, err)
	}
	if value < 0 {
		return 0, false, fmt.Errorf("invalid %s: must be non-negative", name)
	}
	return value, true, nil
}
