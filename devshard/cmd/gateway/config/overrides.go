package config

import (
	"bytes"
	"fmt"

	json "github.com/goccy/go-json"

	"devshard/cmd/gateway/env"
)

// Overrides is the admin-tunable subset of Config, merged over env by Build; a nil field is not overridden.
type Overrides struct {
	env.Tunables
	ParticipantAllowlist            *[]string              `json:"participant_allowlist,omitempty"`
	UnthrottledParticipants         *[]string              `json:"unthrottled_participants,omitempty"`
	FallbackMaxModelLen             *int64                 `json:"fallback_max_model_len,omitempty"`
	HostInputWindowMinRequests      *int64                 `json:"host_input_window_min_requests,omitempty"`
	HostInputWindowInitialRequests  *int64                 `json:"host_input_window_initial_requests,omitempty"`
	HostOutputWindowMinRequests     *int64                 `json:"host_output_window_min_requests,omitempty"`
	HostOutputWindowInitialRequests *int64                 `json:"host_output_window_initial_requests,omitempty"`
	IOAIMDBetaSoft                  *float64               `json:"io_aimd_beta_soft,omitempty"`
	IOAIMDBetaHard                  *float64               `json:"io_aimd_beta_hard,omitempty"`
	IOAIMDBetaSevere                *float64               `json:"io_aimd_beta_severe,omitempty"`
	IOAIMDBetaCross                 *float64               `json:"io_aimd_beta_cross,omitempty"`
	HostCongestionSlack             *float64               `json:"host_congestion_slack,omitempty"`
	HostCutoffAfterFailures         *int64                 `json:"host_cutoff_after_failures,omitempty"`
	HostCutoffMS                    *int64                 `json:"host_cutoff_ms,omitempty"`
	HostCutoffMaxMS                 *int64                 `json:"host_cutoff_max_ms,omitempty"`
	ModelLimits                     map[string]ModelLimits `json:"model_limits,omitempty"`
	ModelAccess                     map[string]string      `json:"model_access,omitempty"`
}

// ParseOverrides rejects unknown fields: a typo in an admin PUT must be reported, never silently ignored.
func ParseOverrides(raw []byte) (Overrides, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var overrides Overrides
	if err := decoder.Decode(&overrides); err != nil {
		return Overrides{}, fmt.Errorf("parsing overrides: %w", err)
	}
	return overrides, nil
}

// ParseStoredOverrides reads back what an earlier build wrote. See README.md, "Overrides".
func ParseStoredOverrides(raw []byte) (Overrides, error) {
	var overrides Overrides
	if err := json.Unmarshal(raw, &overrides); err != nil {
		return Overrides{}, fmt.Errorf("parsing stored overrides: %w", err)
	}
	return overrides, nil
}

func (o Overrides) EncodeOverrides() ([]byte, error) {
	encoded, err := json.Marshal(o)
	if err != nil {
		return nil, fmt.Errorf("encoding overrides: %w", err)
	}
	return encoded, nil
}
