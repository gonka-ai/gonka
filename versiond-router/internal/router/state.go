package router

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	legacySchemaVersion = 1
	SchemaVersion       = 2
)

type HostState string

const (
	HostJoining  HostState = "joining"
	HostActive   HostState = "active"
	HostDraining HostState = "draining"
	HostStopping HostState = "stopping"
	HostOffline  HostState = "offline"
	// HostRemoved is a terminal transition result. Removed memberships are not
	// persisted in Hosts and cannot transition again.
	HostRemoved HostState = "removed"
)

var (
	ErrInvalidTransition  = errors.New("invalid router host state transition")
	ErrLastActiveHost     = errors.New("cannot drain the last active HA host")
	ErrLegacyHost         = errors.New("legacy host owns non-HA versions")
	ErrHostOperation      = errors.New("another router host transfer is in progress")
	ErrOperationOwner     = errors.New("router host transfer belongs to another operation")
	ErrMembershipMismatch = errors.New("router host membership does not match")
	ErrLastConfiguredHost = errors.New("cannot remove the last configured router host")
	identifierPattern     = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

type Host struct {
	MembershipID string    `json:"membership_id"`
	Name         string    `json:"name"`
	Address      string    `json:"address"`
	State        HostState `json:"state"`
}

// Transfer is the durable logical lock for one table-driven membership
// transition. From is checked when the transfer starts; To is its final target.
// The current Host.State selects the next edge handler after a restart.
type Transfer struct {
	ID           string    `json:"id"`
	MembershipID string    `json:"membership_id"`
	Host         string    `json:"host"`
	From         HostState `json:"from"`
	To           HostState `json:"to"`
	Address      string    `json:"address,omitempty"`
	LegacyHost   string    `json:"legacy_host,omitempty"`
	Force        bool      `json:"force,omitempty"`
	Migrated     bool      `json:"migrated,omitempty"`
	StartedAt    time.Time `json:"started_at"`
}

type State struct {
	SchemaVersion  int       `json:"schema_version"`
	Generation     uint64    `json:"generation"`
	Port           int       `json:"port"`
	LegacyHost     string    `json:"legacy_host"`
	NonHAVersions  []string  `json:"non_ha_versions"`
	Hosts          []Host    `json:"hosts"`
	ActiveTransfer *Transfer `json:"active_transfer,omitempty"`
	LastOperation  string    `json:"last_operation,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Transition advances one edge of an existing or newly started transfer.
// From and To describe the immediate edge; Target is the final transfer state.
type Transition struct {
	OperationID  string
	MembershipID string
	Host         string
	From         HostState
	To           HostState
	Target       HostState
	Address      string
	LegacyHost   string
	Force        bool
}

type AddMembership struct {
	OperationID  string
	MembershipID string
	Host         string
	Address      string
}

type CancelTransfer struct {
	OperationID  string
	MembershipID string
	Host         string
}

type TransitionResult struct {
	Changed      bool
	Completed    bool
	Canceled     bool
	MembershipID string
	From         HostState
	To           HostState
	Target       HostState
}

type transitionKey struct {
	from HostState
	to   HostState
}

type transitionHandler func(*State, int, *Transfer) error

type transitionSpec struct {
	targets []HostState
	handler transitionHandler
}

var transitionTable = map[transitionKey]transitionSpec{
	{from: HostActive, to: HostDraining}: {
		targets: []HostState{HostOffline, HostRemoved},
		handler: handleDrain,
	},
	{from: HostDraining, to: HostStopping}: {
		targets: []HostState{HostOffline, HostRemoved},
		handler: handleNoopTransition,
	},
	{from: HostStopping, to: HostOffline}: {
		targets: []HostState{HostOffline, HostRemoved},
		handler: handleNoopTransition,
	},
	{from: HostDraining, to: HostActive}: {
		handler: handleNoopTransition,
	},
	{from: HostOffline, to: HostJoining}: {
		targets: []HostState{HostActive},
		handler: handleJoin,
	},
	{from: HostJoining, to: HostActive}: {
		targets: []HostState{HostActive},
		handler: handleNoopTransition,
	},
	{from: HostOffline, to: HostRemoved}: {
		targets: []HostState{HostRemoved},
		handler: handleRemove,
	},
}

func (s State) Clone() State {
	clone := s
	clone.Hosts = append([]Host(nil), s.Hosts...)
	clone.NonHAVersions = append([]string(nil), s.NonHAVersions...)
	if s.ActiveTransfer != nil {
		transfer := *s.ActiveTransfer
		clone.ActiveTransfer = &transfer
	}
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
		membershipID, err := newMembershipID()
		if err != nil {
			return State{}, err
		}
		state.Hosts = append(state.Hosts, Host{
			MembershipID: membershipID,
			Name:         address,
			Address:      address,
			State:        HostActive,
		})
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
	names := make(map[string]struct{}, len(s.Hosts))
	addresses := make(map[string]struct{}, len(s.Hosts))
	memberships := make(map[string]struct{}, len(s.Hosts))
	for _, host := range s.Hosts {
		if err := validateIdentifier("membership id", host.MembershipID); err != nil {
			return err
		}
		if err := validateIdentifier("host name", host.Name); err != nil {
			return err
		}
		if err := validateIdentifier("host address", host.Address); err != nil {
			return err
		}
		if _, ok := memberships[host.MembershipID]; ok {
			return fmt.Errorf("duplicate membership id %q", host.MembershipID)
		}
		memberships[host.MembershipID] = struct{}{}
		if _, ok := names[host.Name]; ok {
			return fmt.Errorf("duplicate host %q", host.Name)
		}
		names[host.Name] = struct{}{}
		if _, ok := addresses[host.Address]; ok {
			return fmt.Errorf("duplicate host address %q", host.Address)
		}
		addresses[host.Address] = struct{}{}
		if !storedHostState(host.State) {
			return fmt.Errorf("host %q has invalid state %q", host.Name, host.State)
		}
	}
	if _, ok := names[s.LegacyHost]; !ok {
		return fmt.Errorf("legacy host %q is not present", s.LegacyHost)
	}
	for _, version := range s.NonHAVersions {
		if err := validateIdentifier("non-HA version", version); err != nil {
			return err
		}
	}
	if s.ActiveTransfer == nil {
		for _, host := range s.Hosts {
			if transitionalHostState(host.State) {
				return fmt.Errorf("host %q is %s without an active transfer", host.Name, host.State)
			}
		}
		return nil
	}
	return s.validateActiveTransfer()
}

func (s State) validateActiveTransfer() error {
	transfer := s.ActiveTransfer
	if err := validateIdentifier("transfer id", transfer.ID); err != nil {
		return err
	}
	if err := validateIdentifier("transfer membership id", transfer.MembershipID); err != nil {
		return err
	}
	if err := validateIdentifier("transfer host", transfer.Host); err != nil {
		return err
	}
	if !storedHostState(transfer.From) || !transitionTarget(transfer.To) {
		return fmt.Errorf(
			"%w: transfer %s has invalid bounds %s -> %s",
			ErrInvalidTransition,
			transfer.ID,
			transfer.From,
			transfer.To,
		)
	}
	if transfer.Address != "" {
		if err := validateIdentifier("transfer address", transfer.Address); err != nil {
			return err
		}
	}
	if transfer.LegacyHost != "" {
		if err := validateIdentifier("transfer legacy host", transfer.LegacyHost); err != nil {
			return err
		}
	}
	if transfer.StartedAt.IsZero() {
		return fmt.Errorf("transfer %q has no start time", transfer.ID)
	}
	index := s.hostIndex(transfer.Host)
	if index < 0 {
		return fmt.Errorf("active transfer host %q is not present", transfer.Host)
	}
	host := s.Hosts[index]
	if host.MembershipID != transfer.MembershipID {
		return fmt.Errorf(
			"%w: host %s has %s, transfer has %s",
			ErrMembershipMismatch,
			host.Name,
			host.MembershipID,
			transfer.MembershipID,
		)
	}
	if host.State == transfer.To {
		return fmt.Errorf("completed transfer %q remains active", transfer.ID)
	}
	if !hasTransitionToTarget(host.State, transfer.To) {
		return fmt.Errorf(
			"%w: host %s cannot continue from %s to %s",
			ErrInvalidTransition,
			host.Name,
			host.State,
			transfer.To,
		)
	}
	for _, other := range s.Hosts {
		if other.Name != host.Name && transitionalHostState(other.State) {
			return fmt.Errorf(
				"%w: host %s is %s outside transfer %s",
				ErrHostOperation,
				other.Name,
				other.State,
				transfer.ID,
			)
		}
	}
	return nil
}

func (s *State) Add(change AddMembership) (TransitionResult, error) {
	result := TransitionResult{From: "", To: HostJoining, Target: HostActive}
	if err := validateOperation(change.OperationID, change.Host); err != nil {
		return result, err
	}
	if s.ActiveTransfer != nil {
		if s.ActiveTransfer.ID != change.OperationID ||
			s.ActiveTransfer.Host != change.Host ||
			s.ActiveTransfer.From != HostJoining ||
			s.ActiveTransfer.To != HostActive ||
			s.ActiveTransfer.Address != change.Address {
			return result, activeTransferError(s.ActiveTransfer)
		}
		index := s.hostIndex(change.Host)
		if index < 0 || s.Hosts[index].MembershipID != s.ActiveTransfer.MembershipID {
			return result, errors.New("active add transfer has no matching membership")
		}
		if change.MembershipID != "" &&
			change.MembershipID != s.ActiveTransfer.MembershipID {
			return result, membershipMismatch(s.Hosts[index], change.MembershipID)
		}
		result.MembershipID = s.ActiveTransfer.MembershipID
		result.To = s.Hosts[index].State
		return result, nil
	}
	if s.hostIndex(change.Host) >= 0 {
		return result, fmt.Errorf("host %q already exists", change.Host)
	}
	address := change.Address
	if address == "" {
		address = change.Host
	}
	if err := validateIdentifier("host address", address); err != nil {
		return result, err
	}
	if owner := s.hostByAddress(address); owner != "" {
		return result, fmt.Errorf("host address %q already belongs to %s", address, owner)
	}
	membershipID := change.MembershipID
	if membershipID == "" {
		var err error
		membershipID, err = newMembershipID()
		if err != nil {
			return result, err
		}
	}
	if err := validateIdentifier("membership id", membershipID); err != nil {
		return result, err
	}
	if s.hostByMembership(membershipID) != "" {
		return result, fmt.Errorf("membership id %q already exists", membershipID)
	}
	s.Hosts = append(s.Hosts, Host{
		MembershipID: membershipID,
		Name:         change.Host,
		Address:      address,
		State:        HostJoining,
	})
	s.ActiveTransfer = &Transfer{
		ID:           change.OperationID,
		MembershipID: membershipID,
		Host:         change.Host,
		From:         HostJoining,
		To:           HostActive,
		Address:      change.Address,
		StartedAt:    time.Now().UTC(),
	}
	s.commit(change.OperationID)
	result.Changed = true
	result.MembershipID = membershipID
	return result, nil
}

func (s *State) Advance(change Transition) (TransitionResult, error) {
	result := TransitionResult{
		From:   change.From,
		To:     change.To,
		Target: change.Target,
	}
	if err := validateOperation(change.OperationID, change.Host); err != nil {
		return result, err
	}
	if !storedHostState(change.From) || !transitionTarget(change.Target) {
		return result, fmt.Errorf(
			"%w: invalid transfer %s -> %s",
			ErrInvalidTransition,
			change.From,
			change.Target,
		)
	}
	if err := validateTransitionParameters(change); err != nil {
		return result, err
	}
	spec, ok := transitionTable[transitionKey{from: change.From, to: change.To}]
	if !ok {
		return result, fmt.Errorf(
			"%w: no handler for %s -> %s",
			ErrInvalidTransition,
			change.From,
			change.To,
		)
	}
	index := s.hostIndex(change.Host)
	if index < 0 {
		return result, fmt.Errorf("host %q not found", change.Host)
	}
	host := s.Hosts[index]
	result.MembershipID = host.MembershipID
	if change.MembershipID != "" && change.MembershipID != host.MembershipID {
		return result, membershipMismatch(host, change.MembershipID)
	}

	transfer := s.ActiveTransfer
	if transfer == nil {
		if host.State != change.From {
			return result, transitionError(host, change.From, change.To)
		}
		if !spec.allows(change.Target) {
			return result, routeError(host, change.Target, change.To)
		}
		transfer = &Transfer{
			ID:           change.OperationID,
			MembershipID: host.MembershipID,
			Host:         change.Host,
			From:         change.From,
			To:           change.Target,
			Address:      change.Address,
			LegacyHost:   change.LegacyHost,
			Force:        change.Force,
			StartedAt:    time.Now().UTC(),
		}
	} else if err := matchActiveTransfer(transfer, change, host); err != nil {
		return result, err
	}

	if host.State == change.To {
		if !spec.allows(transfer.To) {
			return result, routeError(host, transfer.To, change.To)
		}
		result.Target = transfer.To
		return result, nil
	}
	if host.State != change.From {
		return result, transitionError(host, change.From, change.To)
	}
	if !spec.allows(transfer.To) {
		return result, routeError(host, transfer.To, change.To)
	}
	if err := spec.handler(s, index, transfer); err != nil {
		return result, err
	}
	if change.To != HostRemoved {
		s.Hosts[index].State = change.To
	}
	completed := change.To == transfer.To
	if completed {
		s.ActiveTransfer = nil
	} else {
		s.ActiveTransfer = transfer
	}
	s.commit(change.OperationID)
	result.Changed = true
	result.Completed = completed
	result.Target = transfer.To
	return result, nil
}

func (s *State) Cancel(change CancelTransfer) (TransitionResult, error) {
	result := TransitionResult{
		From:   HostDraining,
		To:     HostActive,
		Target: HostActive,
	}
	if err := validateOperation(change.OperationID, change.Host); err != nil {
		return result, err
	}
	index := s.hostIndex(change.Host)
	if index < 0 {
		return result, fmt.Errorf("host %q not found", change.Host)
	}
	host := s.Hosts[index]
	result.MembershipID = host.MembershipID
	if change.MembershipID != "" && change.MembershipID != host.MembershipID {
		return result, membershipMismatch(host, change.MembershipID)
	}
	transfer := s.ActiveTransfer
	if transfer == nil {
		if host.State == HostActive {
			return result, nil
		}
		return result, errors.New("router host has no active transfer to cancel")
	}
	if transfer.ID != change.OperationID || transfer.Host != change.Host {
		return result, activeTransferError(transfer)
	}
	if host.MembershipID != transfer.MembershipID {
		return result, membershipMismatch(host, transfer.MembershipID)
	}
	if host.State != HostDraining {
		return result, fmt.Errorf(
			"%w: transfer %s cannot be canceled from %s",
			ErrInvalidTransition,
			transfer.ID,
			host.State,
		)
	}
	if err := transitionTable[transitionKey{
		from: HostDraining,
		to:   HostActive,
	}].handler(s, index, transfer); err != nil {
		return result, err
	}
	s.Hosts[index].State = HostActive
	s.ActiveTransfer = nil
	s.commit(change.OperationID)
	result.Changed = true
	result.Completed = true
	result.Canceled = true
	return result, nil
}

func matchActiveTransfer(transfer *Transfer, change Transition, host Host) error {
	if transfer.ID != change.OperationID ||
		transfer.Host != change.Host ||
		transfer.MembershipID != host.MembershipID {
		return activeTransferError(transfer)
	}
	if change.MembershipID != "" && change.MembershipID != transfer.MembershipID {
		return membershipMismatch(host, change.MembershipID)
	}
	if transfer.To != change.Target {
		return fmt.Errorf(
			"%w: transfer %s target changed",
			ErrOperationOwner,
			transfer.ID,
		)
	}
	if transfer.Migrated {
		if change.Address != "" && change.Address != host.Address {
			return fmt.Errorf(
				"%w: migrated transfer %s address changed",
				ErrOperationOwner,
				transfer.ID,
			)
		}
		return nil
	}
	if transfer.Address != change.Address ||
		transfer.LegacyHost != change.LegacyHost ||
		transfer.Force != change.Force {
		return fmt.Errorf(
			"%w: transfer %s parameters changed",
			ErrOperationOwner,
			transfer.ID,
		)
	}
	return nil
}

func handleDrain(s *State, index int, transfer *Transfer) error {
	if transfer.To == HostRemoved {
		if err := validateRemoval(s, index, transfer); err != nil {
			return err
		}
	}
	if s.activeHosts() <= 1 && !transfer.Force {
		return ErrLastActiveHost
	}
	if transfer.Host == s.LegacyHost && len(s.NonHAVersions) > 0 && !transfer.Force {
		return fmt.Errorf("%w: %s", ErrLegacyHost, strings.Join(s.NonHAVersions, ","))
	}
	return nil
}

func handleJoin(s *State, index int, transfer *Transfer) error {
	address := transfer.Address
	if address == "" {
		address = s.Hosts[index].Address
	}
	if err := validateIdentifier("host address", address); err != nil {
		return err
	}
	if owner := s.hostByAddressExcept(address, index); owner != "" {
		return fmt.Errorf("host address %q already belongs to %s", address, owner)
	}
	s.Hosts[index].Address = address
	return nil
}

func handleRemove(s *State, index int, transfer *Transfer) error {
	if err := validateRemoval(s, index, transfer); err != nil {
		return err
	}
	host := s.Hosts[index]
	if host.Name == s.LegacyHost {
		s.LegacyHost = transfer.LegacyHost
	}
	s.Hosts = append(s.Hosts[:index], s.Hosts[index+1:]...)
	return nil
}

func validateRemoval(s *State, index int, transfer *Transfer) error {
	host := s.Hosts[index]
	if len(s.Hosts) <= 1 {
		return ErrLastConfiguredHost
	}
	if host.Name != s.LegacyHost {
		if transfer.LegacyHost != "" {
			return errors.New(
				"replacement legacy host is valid only when removing the current legacy host",
			)
		}
		return nil
	}
	if transfer.LegacyHost == "" {
		return errors.New("removing the legacy host requires a replacement legacy host")
	}
	if len(s.NonHAVersions) > 0 && !transfer.Force {
		return fmt.Errorf(
			"%w: migrate %s and retry with force",
			ErrLegacyHost,
			strings.Join(s.NonHAVersions, ","),
		)
	}
	legacyIndex := s.hostIndex(transfer.LegacyHost)
	if legacyIndex < 0 || s.Hosts[legacyIndex].State != HostActive {
		return fmt.Errorf(
			"replacement legacy host %q must be active",
			transfer.LegacyHost,
		)
	}
	return nil
}

func handleNoopTransition(*State, int, *Transfer) error {
	return nil
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

func (s State) hostByAddress(address string) string {
	return s.hostByAddressExcept(address, -1)
}

func (s State) hostByAddressExcept(address string, except int) string {
	for i, host := range s.Hosts {
		if i != except && host.Address == address {
			return host.Name
		}
	}
	return ""
}

func (s State) hostByMembership(membershipID string) string {
	for _, host := range s.Hosts {
		if host.MembershipID == membershipID {
			return host.Name
		}
	}
	return ""
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

func storedHostState(state HostState) bool {
	switch state {
	case HostJoining, HostActive, HostDraining, HostStopping, HostOffline:
		return true
	default:
		return false
	}
}

func transitionalHostState(state HostState) bool {
	return state == HostJoining || state == HostDraining || state == HostStopping
}

func transitionTarget(state HostState) bool {
	return state == HostActive || state == HostOffline || state == HostRemoved
}

func validateTransitionParameters(change Transition) error {
	switch change.Target {
	case HostActive:
		if change.LegacyHost != "" {
			return errors.New("legacy host is valid only for a removal transfer")
		}
		if change.Force {
			return errors.New("force is valid only for a stop or removal transfer")
		}
	case HostOffline:
		if change.Address != "" {
			return errors.New("address is valid only for an activation transfer")
		}
		if change.LegacyHost != "" {
			return errors.New("legacy host is valid only for a removal transfer")
		}
	case HostRemoved:
		if change.Address != "" {
			return errors.New("address is valid only for an activation transfer")
		}
	}
	return nil
}

func (spec transitionSpec) allows(target HostState) bool {
	for _, allowed := range spec.targets {
		if allowed == target {
			return true
		}
	}
	return false
}

func hasTransitionToTarget(from, target HostState) bool {
	for key, spec := range transitionTable {
		if key.from == from && spec.allows(target) {
			return true
		}
	}
	return false
}

func validateOperation(operationID, host string) error {
	if err := validateIdentifier("operation id", operationID); err != nil {
		return err
	}
	return validateIdentifier("host", host)
}

func transitionError(host Host, from, to HostState) error {
	return fmt.Errorf(
		"%w: host %s is %s, expected %s for %s -> %s",
		ErrInvalidTransition,
		host.Name,
		host.State,
		from,
		from,
		to,
	)
}

func routeError(host Host, target, requested HostState) error {
	return fmt.Errorf(
		"%w: host %s cannot advance from %s to %s on the path to %s",
		ErrInvalidTransition,
		host.Name,
		host.State,
		requested,
		target,
	)
}

func activeTransferError(transfer *Transfer) error {
	return fmt.Errorf(
		"%w: transfer %s owns membership %s (%s)",
		ErrHostOperation,
		transfer.ID,
		transfer.MembershipID,
		transfer.Host,
	)
}

func membershipMismatch(host Host, expected string) error {
	return fmt.Errorf(
		"%w: host %s has %s, expected %s",
		ErrMembershipMismatch,
		host.Name,
		host.MembershipID,
		expected,
	)
}

func newMembershipID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate membership id: %w", err)
	}
	return "membership-" + hex.EncodeToString(raw[:]), nil
}

func validateIdentifier(kind, value string) error {
	if value == "" || !identifierPattern.MatchString(value) {
		return fmt.Errorf("invalid %s %q", kind, value)
	}
	return nil
}
