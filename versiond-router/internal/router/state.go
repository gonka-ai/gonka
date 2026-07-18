package router

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

const SchemaVersion = 1

type HostState string

const (
	HostActive   HostState = "active"
	HostDraining HostState = "draining"
	HostOffline  HostState = "offline"
	HostJoining  HostState = "joining"
)

var (
	ErrInvalidTransition = errors.New("invalid router host state transition")
	ErrLastActiveHost    = errors.New("cannot drain the last active HA host")
	ErrLegacyHost        = errors.New("legacy host owns non-HA versions")
	ErrHostOperation     = errors.New("another router host operation is in progress")
	ErrOperationOwner    = errors.New("router host transition belongs to another operation")
	identifierPattern    = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

type Host struct {
	Name        string    `json:"name"`
	Address     string    `json:"address"`
	State       HostState `json:"state"`
	OperationID string    `json:"operation_id,omitempty"`
}

type State struct {
	SchemaVersion int       `json:"schema_version"`
	Generation    uint64    `json:"generation"`
	Port          int       `json:"port"`
	LegacyHost    string    `json:"legacy_host"`
	NonHAVersions []string  `json:"non_ha_versions"`
	Hosts         []Host    `json:"hosts"`
	LastOperation string    `json:"last_operation,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Action string

const (
	ActionDrain    Action = "drain"
	ActionOffline  Action = "offline"
	ActionJoin     Action = "join"
	ActionActivate Action = "activate"
)

type Transition struct {
	Action      Action
	Host        string
	Address     string
	Force       bool
	OperationID string
}

func (s State) Clone() State {
	clone := s
	clone.Hosts = append([]Host(nil), s.Hosts...)
	clone.NonHAVersions = append([]string(nil), s.NonHAVersions...)
	return clone
}

func NewState(hosts []string, port int, legacyHost string, nonHAVersions []string) (State, error) {
	if port <= 0 || port > 65535 {
		return State{}, fmt.Errorf("invalid upstream port %d", port)
	}
	if len(hosts) == 0 {
		return State{}, errors.New("at least one versiond host is required")
	}
	state := State{
		SchemaVersion: SchemaVersion,
		Generation:    1,
		Port:          port,
		LegacyHost:    legacyHost,
		NonHAVersions: append([]string(nil), nonHAVersions...),
		UpdatedAt:     time.Now().UTC(),
	}
	seen := make(map[string]struct{}, len(hosts))
	for _, address := range hosts {
		if err := validateIdentifier("host", address); err != nil {
			return State{}, err
		}
		if _, ok := seen[address]; ok {
			return State{}, fmt.Errorf("duplicate versiond host %q", address)
		}
		seen[address] = struct{}{}
		state.Hosts = append(state.Hosts, Host{Name: address, Address: address, State: HostActive})
	}
	if state.LegacyHost == "" {
		state.LegacyHost = state.Hosts[0].Name
	}
	if _, ok := seen[state.LegacyHost]; !ok {
		return State{}, fmt.Errorf("legacy host %q is not in VERSIOND_HOSTS", state.LegacyHost)
	}
	for _, version := range state.NonHAVersions {
		if err := validateIdentifier("non-HA version", version); err != nil {
			return State{}, err
		}
	}
	state.sort()
	return state, nil
}

func (s State) Validate() error {
	if s.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported state schema %d", s.SchemaVersion)
	}
	if s.Port <= 0 || s.Port > 65535 {
		return fmt.Errorf("invalid upstream port %d", s.Port)
	}
	if len(s.Hosts) == 0 {
		return errors.New("router state has no hosts")
	}
	seen := make(map[string]struct{}, len(s.Hosts))
	addresses := make(map[string]struct{}, len(s.Hosts))
	transitioning := ""
	for _, host := range s.Hosts {
		if err := validateIdentifier("host name", host.Name); err != nil {
			return err
		}
		if err := validateIdentifier("host address", host.Address); err != nil {
			return err
		}
		if _, ok := seen[host.Name]; ok {
			return fmt.Errorf("duplicate host %q", host.Name)
		}
		seen[host.Name] = struct{}{}
		if _, ok := addresses[host.Address]; ok {
			return fmt.Errorf("duplicate host address %q", host.Address)
		}
		addresses[host.Address] = struct{}{}
		switch host.State {
		case HostDraining, HostJoining:
			if host.OperationID == "" {
				return fmt.Errorf("host %q has no transition operation id", host.Name)
			}
			if transitioning != "" {
				return fmt.Errorf(
					"%w: %s and %s",
					ErrHostOperation,
					transitioning,
					host.Name,
				)
			}
			transitioning = host.Name
		case HostActive, HostOffline:
			if host.OperationID != "" {
				return fmt.Errorf("stable host %q retains transition operation id", host.Name)
			}
		default:
			return fmt.Errorf("host %q has invalid state %q", host.Name, host.State)
		}
	}
	if _, ok := seen[s.LegacyHost]; !ok {
		return fmt.Errorf("legacy host %q is not present", s.LegacyHost)
	}
	for _, version := range s.NonHAVersions {
		if err := validateIdentifier("non-HA version", version); err != nil {
			return err
		}
	}
	return nil
}

func (s *State) Apply(change Transition) (bool, HostState, HostState, error) {
	if change.OperationID == "" {
		return false, "", "", errors.New("operation id is required")
	}
	if err := validateIdentifier("host", change.Host); err != nil {
		return false, "", "", err
	}
	index := s.hostIndex(change.Host)
	if index < 0 && change.Action != ActionJoin {
		return false, "", "", fmt.Errorf("host %q not found", change.Host)
	}

	if index < 0 {
		if active := s.transitioningHost(""); active != "" {
			return false, "", "", fmt.Errorf("%w: %s", ErrHostOperation, active)
		}
		address := change.Address
		if address == "" {
			address = change.Host
		}
		if err := validateIdentifier("host address", address); err != nil {
			return false, "", "", err
		}
		for _, host := range s.Hosts {
			if host.Address == address {
				return false, "", "", fmt.Errorf("host address %q already belongs to %s", address, host.Name)
			}
		}
		s.Hosts = append(s.Hosts, Host{
			Name:        change.Host,
			Address:     address,
			State:       HostJoining,
			OperationID: change.OperationID,
		})
		s.commit(change.OperationID)
		return true, "", HostJoining, nil
	}

	host := &s.Hosts[index]
	from := host.State
	if err := validateOperationOwner(*host, change); err != nil {
		return false, from, from, err
	}
	to, err := s.nextState(*host, change)
	if err != nil {
		return false, from, from, err
	}
	if to == from {
		return false, from, to, nil
	}
	if change.Address != "" {
		if err := validateIdentifier("host address", change.Address); err != nil {
			return false, from, from, err
		}
		for i, existing := range s.Hosts {
			if i != index && existing.Address == change.Address {
				return false, from, from, fmt.Errorf(
					"host address %q already belongs to %s",
					change.Address,
					existing.Name,
				)
			}
		}
		host.Address = change.Address
	}
	host.State = to
	switch to {
	case HostDraining, HostJoining:
		host.OperationID = change.OperationID
	default:
		host.OperationID = ""
	}
	s.commit(change.OperationID)
	return true, from, to, nil
}

func validateOperationOwner(host Host, change Transition) error {
	if host.State != HostDraining && host.State != HostJoining {
		return nil
	}
	if host.OperationID == change.OperationID || change.Force {
		return nil
	}
	return fmt.Errorf(
		"%w: host %s is owned by %s",
		ErrOperationOwner,
		host.Name,
		host.OperationID,
	)
}

func (s State) nextState(host Host, change Transition) (HostState, error) {
	switch change.Action {
	case ActionDrain:
		if host.State == HostDraining {
			return host.State, nil
		}
		if host.State != HostActive {
			return host.State, transitionError(host, HostDraining)
		}
		if active := s.transitioningHost(host.Name); active != "" {
			return host.State, fmt.Errorf("%w: %s", ErrHostOperation, active)
		}
		if s.activeHosts() <= 1 && !change.Force {
			return host.State, ErrLastActiveHost
		}
		if host.Name == s.LegacyHost && len(s.NonHAVersions) > 0 && !change.Force {
			return host.State, fmt.Errorf("%w: %s", ErrLegacyHost, strings.Join(s.NonHAVersions, ","))
		}
		return HostDraining, nil
	case ActionOffline:
		if host.State == HostOffline {
			return host.State, nil
		}
		if host.State != HostDraining {
			return host.State, transitionError(host, HostOffline)
		}
		return HostOffline, nil
	case ActionJoin:
		if host.State == HostJoining {
			return host.State, nil
		}
		if host.State != HostOffline {
			return host.State, transitionError(host, HostJoining)
		}
		if active := s.transitioningHost(host.Name); active != "" {
			return host.State, fmt.Errorf("%w: %s", ErrHostOperation, active)
		}
		return HostJoining, nil
	case ActionActivate:
		if host.State == HostActive {
			return host.State, nil
		}
		if host.State != HostJoining && host.State != HostDraining {
			return host.State, transitionError(host, HostActive)
		}
		return HostActive, nil
	default:
		return host.State, fmt.Errorf("unknown host action %q", change.Action)
	}
}

func transitionError(host Host, to HostState) error {
	return fmt.Errorf("%w: host %s: %s -> %s", ErrInvalidTransition, host.Name, host.State, to)
}

func (s *State) commit(operationID string) {
	s.Generation++
	s.LastOperation = operationID
	s.UpdatedAt = time.Now().UTC()
	s.sort()
}

func (s *State) sort() {
	sort.Slice(s.Hosts, func(i, j int) bool { return s.Hosts[i].Name < s.Hosts[j].Name })
	sort.Strings(s.NonHAVersions)
}

func (s State) hostIndex(name string) int {
	for i := range s.Hosts {
		if s.Hosts[i].Name == name {
			return i
		}
	}
	return -1
}

func (s State) activeHosts() int {
	count := 0
	for _, host := range s.Hosts {
		if host.State == HostActive {
			count++
		}
	}
	return count
}

func (s State) transitioningHost(exclude string) string {
	for _, host := range s.Hosts {
		if host.Name == exclude {
			continue
		}
		if host.State == HostDraining || host.State == HostJoining {
			return host.Name
		}
	}
	return ""
}

func validateIdentifier(kind, value string) error {
	if value == "" || !identifierPattern.MatchString(value) {
		return fmt.Errorf("invalid %s %q", kind, value)
	}
	return nil
}
