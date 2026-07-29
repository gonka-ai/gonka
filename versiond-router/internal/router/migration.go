package router

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type stateEnvelope struct {
	SchemaVersion int `json:"schema_version"`
}

type stateV1 struct {
	SchemaVersion  int       `json:"schema_version"`
	Generation     uint64    `json:"generation"`
	Port           int       `json:"port"`
	LegacyHost     string    `json:"legacy_host"`
	NonHAVersions  []string  `json:"non_ha_versions"`
	Hosts          []hostV1  `json:"hosts"`
	ActiveTransfer *Transfer `json:"active_transfer,omitempty"`
	LastOperation  string    `json:"last_operation,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type hostV1 struct {
	MembershipID string    `json:"membership_id,omitempty"`
	Name         string    `json:"name"`
	Address      string    `json:"address"`
	State        HostState `json:"state"`
	OperationID  string    `json:"operation_id,omitempty"`
}

func decodeState(data []byte) (State, bool, error) {
	var envelope stateEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return State{}, false, err
	}
	switch envelope.SchemaVersion {
	case SchemaVersion:
		var state State
		if err := json.Unmarshal(data, &state); err != nil {
			return State{}, false, err
		}
		if err := state.Validate(); err != nil {
			return State{}, false, err
		}
		return state, false, nil
	case legacySchemaVersion:
		state, err := migrateStateV1(data)
		return state, err == nil, err
	default:
		return State{}, false, fmt.Errorf(
			"unsupported state schema %d",
			envelope.SchemaVersion,
		)
	}
}

func migrateStateV1(data []byte) (State, error) {
	var legacy stateV1
	if err := json.Unmarshal(data, &legacy); err != nil {
		return State{}, err
	}
	state := State{
		SchemaVersion: SchemaVersion,
		Generation:    legacy.Generation,
		Port:          legacy.Port,
		LegacyHost:    legacy.LegacyHost,
		NonHAVersions: append([]string(nil), legacy.NonHAVersions...),
		LastOperation: legacy.LastOperation,
		UpdatedAt:     legacy.UpdatedAt,
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now().UTC()
	}
	legacyOperations := make(map[string]string)
	for _, host := range legacy.Hosts {
		membershipID := host.MembershipID
		if membershipID == "" {
			membershipID = migratedMembershipID(host.Name, host.Address)
		}
		state.Hosts = append(state.Hosts, Host{
			MembershipID: membershipID,
			Name:         host.Name,
			Address:      host.Address,
			State:        host.State,
		})
		if host.OperationID != "" {
			legacyOperations[host.Name] = host.OperationID
		}
	}
	if legacy.ActiveTransfer != nil {
		transfer := *legacy.ActiveTransfer
		index := state.hostIndex(transfer.Host)
		if index < 0 {
			return State{}, fmt.Errorf(
				"legacy active transfer host %q is not present",
				transfer.Host,
			)
		}
		if transfer.MembershipID == "" {
			transfer.MembershipID = state.Hosts[index].MembershipID
		}
		if transfer.StartedAt.IsZero() {
			transfer.StartedAt = state.UpdatedAt
		}
		state.ActiveTransfer = &transfer
	} else if err := restoreLegacyTransfer(&state, legacyOperations); err != nil {
		return State{}, err
	}
	state.sort()
	if err := state.Validate(); err != nil {
		return State{}, fmt.Errorf("migrate router state schema 1: %w", err)
	}
	return state, nil
}

func restoreLegacyTransfer(state *State, operations map[string]string) error {
	var transitional *Host
	for i := range state.Hosts {
		host := &state.Hosts[i]
		if !transitionalHostState(host.State) {
			if operations[host.Name] != "" {
				return fmt.Errorf(
					"stable legacy host %q retains operation id",
					host.Name,
				)
			}
			continue
		}
		if transitional != nil {
			return errors.New("legacy state has multiple transitional hosts")
		}
		transitional = host
	}
	if transitional == nil {
		return nil
	}
	operationID := operations[transitional.Name]
	if operationID == "" {
		return fmt.Errorf(
			"legacy transitional host %q has no operation id",
			transitional.Name,
		)
	}
	transfer := &Transfer{
		ID:           operationID,
		MembershipID: transitional.MembershipID,
		Host:         transitional.Name,
		Migrated:     true,
		StartedAt:    state.UpdatedAt,
	}
	switch transitional.State {
	case HostDraining:
		transfer.From = HostActive
		transfer.To = HostOffline
	case HostJoining:
		transfer.From = HostJoining
		transfer.To = HostActive
	default:
		return fmt.Errorf(
			"legacy host %q has unsupported transitional state %q",
			transitional.Name,
			transitional.State,
		)
	}
	state.ActiveTransfer = transfer
	return nil
}

func migratedMembershipID(name, address string) string {
	sum := sha256.Sum256([]byte("router-state-v1\x00" + name + "\x00" + address))
	return "membership-migrated-" + hex.EncodeToString(sum[:16])
}
