package hostctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type runtimeCommand func(context.Context, ...string) (string, error)

type serviceState string

const (
	serviceRunning serviceState = "running"
	serviceStopped serviceState = "stopped"
	serviceAbsent  serviceState = "absent"
)

type serviceRuntime interface {
	ValidateStopContract(context.Context, time.Duration) error
	State(context.Context) (serviceState, error)
	Signal(context.Context, string) error
	Start(context.Context, string) error
	RestartPolicy(context.Context) (string, error)
	SetRestartPolicy(context.Context, string) error
}

func newServiceRuntime(kind Runtime, service string, run runtimeCommand) serviceRuntime {
	if kind == RuntimeSystemd {
		return systemdRuntime{service: service, run: run}
	}
	return dockerRuntime{service: service, run: run}
}

type dockerRuntime struct {
	service string
	run     runtimeCommand
}

func (r dockerRuntime) ValidateStopContract(ctx context.Context, minimum time.Duration) error {
	output, err := r.run(
		ctx,
		"docker", "inspect", "--format", "{{json .Config.StopTimeout}}", r.service,
	)
	if err != nil {
		return fmt.Errorf("inspect Docker stop timeout: %w", err)
	}
	raw := strings.TrimSpace(output)
	if raw == "" || raw == "null" || raw == "0" {
		return fmt.Errorf(
			"Docker service %s has no external stop timeout; configure stop_grace_period >= %s",
			r.service,
			minimum,
		)
	}
	seconds, err := strconv.ParseInt(strings.Trim(raw, `"`), 10, 64)
	if err != nil || seconds <= 0 {
		return fmt.Errorf("parse Docker stop timeout %q", raw)
	}
	actual := time.Duration(seconds) * time.Second
	if actual < minimum {
		return fmt.Errorf(
			"Docker external stop timeout for %s is %s, require at least %s",
			r.service,
			actual,
			minimum,
		)
	}
	return nil
}

func (r dockerRuntime) State(ctx context.Context) (serviceState, error) {
	output, err := r.run(
		ctx,
		"docker", "inspect", "--format", "{{.State.Running}}", r.service,
	)
	if err != nil {
		if dockerServiceAbsent(err, r.service) {
			return serviceAbsent, nil
		}
		return "", err
	}
	switch strings.TrimSpace(output) {
	case "true":
		return serviceRunning, nil
	case "false":
		return serviceStopped, nil
	default:
		return "", fmt.Errorf("unexpected Docker running state %q", strings.TrimSpace(output))
	}
}

func dockerServiceAbsent(err error, service string) bool {
	message := strings.ToLower(err.Error())
	service = strings.ToLower(strings.TrimSpace(service))
	for _, prefix := range []string{
		"no such object: ",
		"no such container: ",
	} {
		rest := message
		needle := prefix + service
		for {
			index := strings.Index(rest, needle)
			if index < 0 {
				break
			}
			end := index + len(needle)
			if end == len(rest) || !dockerServiceNameByte(rest[end]) {
				return true
			}
			rest = rest[end:]
		}
	}
	return false
}

func dockerServiceNameByte(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= '0' && value <= '9' ||
		value == '_' ||
		value == '.' ||
		value == '-'
}

func (r dockerRuntime) Signal(ctx context.Context, signal string) error {
	_, err := r.run(ctx, "docker", "kill", "--signal", signal, r.service)
	return err
}

func (r dockerRuntime) Start(ctx context.Context, restartPolicy string) error {
	if restartPolicy == "" {
		return errors.New("Docker restart policy is required for replacement")
	}
	if err := r.SetRestartPolicy(ctx, restartPolicy); err != nil {
		return err
	}
	_, err := r.run(ctx, "docker", "start", r.service)
	return err
}

func (r dockerRuntime) RestartPolicy(ctx context.Context) (string, error) {
	output, err := r.run(
		ctx,
		"docker", "inspect", "--format", "{{json .HostConfig.RestartPolicy}}", r.service,
	)
	if err != nil {
		return "", err
	}
	var policy struct {
		Name              string `json:"Name"`
		MaximumRetryCount int    `json:"MaximumRetryCount"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &policy); err != nil {
		return "", fmt.Errorf("decode Docker restart policy: %w", err)
	}
	value := policy.Name
	if value == "" {
		value = "no"
	}
	if value == "on-failure" && policy.MaximumRetryCount > 0 {
		value += ":" + strconv.Itoa(policy.MaximumRetryCount)
	}
	if err := validateDockerRestartPolicy(value); err != nil {
		return "", err
	}
	return value, nil
}

func (r dockerRuntime) SetRestartPolicy(ctx context.Context, policy string) error {
	if err := validateDockerRestartPolicy(policy); err != nil {
		return err
	}
	_, err := r.run(ctx, "docker", "update", "--restart="+policy, r.service)
	return err
}

type systemdRuntime struct {
	service string
	run     runtimeCommand
}

func (r systemdRuntime) ValidateStopContract(ctx context.Context, minimum time.Duration) error {
	output, err := r.run(
		ctx,
		"systemctl", "show", r.service,
		"--property=TimeoutStopUSec", "--property=KillMode", "--property=SendSIGKILL",
	)
	if err != nil {
		return fmt.Errorf("inspect systemd stop contract: %w", err)
	}
	properties := parseSystemdProperties(output)
	timeout, err := parseSystemdTimeSpan(properties["TimeoutStopUSec"])
	if err != nil {
		return fmt.Errorf(
			"parse systemd TimeoutStopUSec (configured by TimeoutStopSec) for %s: %w",
			r.service,
			err,
		)
	}
	if timeout < minimum {
		return fmt.Errorf(
			"systemd TimeoutStopSec for %s is %s, require at least %s",
			r.service,
			timeout,
			minimum,
		)
	}
	killMode := properties["KillMode"]
	if killMode != "mixed" {
		return fmt.Errorf(
			"systemd KillMode for %s is %q, require mixed so versiond drains its children before the final cgroup kill",
			r.service,
			killMode,
		)
	}
	if properties["SendSIGKILL"] != "yes" {
		return fmt.Errorf("systemd SendSIGKILL for %s must be yes", r.service)
	}
	return nil
}

func (r systemdRuntime) State(ctx context.Context) (serviceState, error) {
	output, err := r.run(
		ctx,
		"systemctl", "show", r.service,
		"--property=LoadState", "--property=ActiveState",
	)
	properties := parseSystemdProperties(output)
	if properties["LoadState"] == "not-found" {
		return serviceAbsent, nil
	}
	if err != nil {
		return "", err
	}
	switch properties["ActiveState"] {
	case "inactive", "failed":
		return serviceStopped, nil
	case "active", "activating", "deactivating", "reloading", "refreshing", "maintenance":
		return serviceRunning, nil
	default:
		return "", fmt.Errorf(
			"unexpected systemd active state %q with load state %q",
			properties["ActiveState"],
			properties["LoadState"],
		)
	}
}

func (r systemdRuntime) Signal(ctx context.Context, signal string) error {
	var args []string
	if signal == "TERM" {
		// A stop job suppresses Restart= while still honoring TimeoutStopSec and
		// KillMode. A raw systemctl kill would allow the unit to resurrect.
		args = []string{"systemctl", "stop", "--no-block", r.service}
	} else {
		args = []string{"systemctl", "kill", "--kill-who=all", "--signal", signal, r.service}
	}
	_, err := r.run(ctx, args...)
	return err
}

func (r systemdRuntime) Start(ctx context.Context, _ string) error {
	_, err := r.run(ctx, "systemctl", "start", r.service)
	return err
}

func (systemdRuntime) RestartPolicy(context.Context) (string, error) {
	return "", errors.New("restart policy is managed by the systemd unit")
}

func (systemdRuntime) SetRestartPolicy(context.Context, string) error {
	return errors.New("restart policy is managed by the systemd unit")
}

func parseSystemdProperties(output string) map[string]string {
	properties := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok {
			properties[key] = value
		}
	}
	return properties
}

var systemdTimePart = regexp.MustCompile(
	`([0-9]+(?:\.[0-9]+)?)(` +
		`nsec|ns|usec|us|msec|ms|seconds|second|sec|s|` +
		`months|month|M|minutes|minute|min|m|hours|hour|hr|h|` +
		`days|day|d|weeks|week|w|years|year|y)`,
)

func parseSystemdTimeSpan(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "infinity" {
		return time.Duration(math.MaxInt64), nil
	}
	compact := strings.ReplaceAll(raw, " ", "")
	matches := systemdTimePart.FindAllStringSubmatch(compact, -1)
	if len(matches) == 0 {
		return 0, fmt.Errorf("unsupported value %q", raw)
	}
	consumed := ""
	var total time.Duration
	for _, match := range matches {
		consumed += match[0]
		value, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			return 0, err
		}
		unit, ok := systemdTimeUnit(match[2])
		if !ok {
			return 0, fmt.Errorf("unsupported unit %q", match[2])
		}
		part := value * float64(unit)
		if part >= float64(math.MaxInt64-total) {
			return time.Duration(math.MaxInt64), nil
		}
		total += time.Duration(part)
	}
	if consumed != compact {
		return 0, fmt.Errorf("unsupported value %q", raw)
	}
	return total, nil
}

func systemdTimeUnit(raw string) (time.Duration, bool) {
	day := 24 * time.Hour
	switch raw {
	case "nsec", "ns":
		return time.Nanosecond, true
	case "usec", "us":
		return time.Microsecond, true
	case "msec", "ms":
		return time.Millisecond, true
	case "seconds", "second", "sec", "s":
		return time.Second, true
	case "minutes", "minute", "min", "m":
		return time.Minute, true
	case "hours", "hour", "hr", "h":
		return time.Hour, true
	case "days", "day", "d":
		return day, true
	case "weeks", "week", "w":
		return 7 * day, true
	case "months", "month", "M":
		return time.Duration(30.44 * float64(day)), true
	case "years", "year", "y":
		return time.Duration(365.25 * float64(day)), true
	default:
		return 0, false
	}
}
