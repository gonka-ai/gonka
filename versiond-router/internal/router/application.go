package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

const appliedStateSchemaVersion = 1

type appliedState struct {
	SchemaVersion int       `json:"schema_version"`
	Generation    uint64    `json:"generation"`
	ConfigSHA     string    `json:"config_sha256"`
	OperationID   string    `json:"operation_id"`
	AppliedAt     time.Time `json:"applied_at"`
}

type ApplicationStatus struct {
	DesiredGeneration uint64 `json:"desired_generation"`
	AppliedGeneration uint64 `json:"applied_generation,omitempty"`
	DesiredConfigSHA  string `json:"desired_config_sha256"`
	AppliedConfigSHA  string `json:"applied_config_sha256,omitempty"`
	OutputConfigSHA   string `json:"output_config_sha256,omitempty"`
	Converged         bool   `json:"converged"`
}

// reconcile projects one committed desired generation into nginx. Every step
// is safe to repeat after a crash.
func (c *Controller) reconcile(
	ctx context.Context,
	state State,
	config []byte,
	operationID string,
	reload bool,
) error {
	if err := state.Validate(); err != nil {
		return fmt.Errorf("validate desired router state: %w", err)
	}
	if len(config) == 0 {
		return errors.New("desired nginx config is empty")
	}
	desiredSHA := hashBytes(config)
	applied, err := c.loadAppliedState()
	if err != nil {
		return err
	}
	output, outputErr := os.ReadFile(c.config.OutputPath)
	if outputErr != nil && !errors.Is(outputErr, fs.ErrNotExist) {
		return outputErr
	}
	outputMatches := outputErr == nil && hashBytes(output) == desiredSHA
	appliedMatches := applied != nil &&
		applied.Generation == state.Generation &&
		applied.ConfigSHA == desiredSHA

	if !outputMatches {
		if err := writeFileAtomic(c.config.OutputPath, config, 0o644); err != nil {
			return fmt.Errorf("publish desired nginx config: %w", err)
		}
	}
	if err := c.runner.Run(ctx, c.config.NginxBinary, "-t"); err != nil {
		validationErr := fmt.Errorf("validate desired nginx config: %w", err)
		if outputMatches {
			return validationErr
		}
		if restoreErr := c.restoreOutputProjection(output, outputErr == nil); restoreErr != nil {
			return errors.Join(validationErr, restoreErr)
		}
		return validationErr
	}
	if reload && (!outputMatches || !appliedMatches) {
		if err := c.runner.Run(
			ctx,
			c.config.NginxBinary,
			"-s",
			"reload",
		); err != nil {
			return fmt.Errorf("reload desired nginx config: %w", err)
		}
	}
	if outputMatches && appliedMatches {
		return nil
	}
	return writeJSONAtomic(c.config.AppliedPath, appliedState{
		SchemaVersion: appliedStateSchemaVersion,
		Generation:    state.Generation,
		ConfigSHA:     desiredSHA,
		OperationID:   operationID,
		AppliedAt:     time.Now().UTC(),
	}, 0o600)
}

func (c *Controller) restoreOutputProjection(data []byte, existed bool) error {
	if existed {
		if err := writeFileAtomic(c.config.OutputPath, data, 0o644); err != nil {
			return fmt.Errorf("restore previous nginx config: %w", err)
		}
		return nil
	}
	if err := os.Remove(c.config.OutputPath); err != nil &&
		!errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove rejected nginx config: %w", err)
	}
	return nil
}

func (c *Controller) applicationStatus(state State) (ApplicationStatus, error) {
	template, err := os.ReadFile(c.config.TemplatePath)
	if err != nil {
		return ApplicationStatus{}, err
	}
	desired, err := Render(template, state, c.config.ProxyPolicy)
	if err != nil {
		return ApplicationStatus{}, err
	}
	return c.applicationStatusForConfig(state, desired)
}

func (c *Controller) applicationStatusForConfig(
	state State,
	desired []byte,
) (ApplicationStatus, error) {
	status := ApplicationStatus{
		DesiredGeneration: state.Generation,
		DesiredConfigSHA:  hashBytes(desired),
	}
	applied, err := c.loadAppliedState()
	if err != nil {
		return ApplicationStatus{}, err
	}
	if applied != nil {
		status.AppliedGeneration = applied.Generation
		status.AppliedConfigSHA = applied.ConfigSHA
	}
	output, err := os.ReadFile(c.config.OutputPath)
	if err == nil {
		status.OutputConfigSHA = hashBytes(output)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return ApplicationStatus{}, err
	}
	status.Converged = applied != nil &&
		applied.Generation == status.DesiredGeneration &&
		applied.ConfigSHA == status.DesiredConfigSHA &&
		status.OutputConfigSHA == status.DesiredConfigSHA
	return status, nil
}

func (c *Controller) loadAppliedState() (*appliedState, error) {
	data, err := os.ReadFile(c.config.AppliedPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state appliedState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode applied router state: %w", err)
	}
	if state.SchemaVersion != appliedStateSchemaVersion {
		return nil, fmt.Errorf(
			"unsupported applied state schema %d",
			state.SchemaVersion,
		)
	}
	if state.Generation == 0 || state.ConfigSHA == "" ||
		state.OperationID == "" || state.AppliedAt.IsZero() {
		return nil, errors.New("applied router state is incomplete")
	}
	return &state, nil
}
