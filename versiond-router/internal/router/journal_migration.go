package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

type operationJournalEnvelope struct {
	SchemaVersion int `json:"schema_version"`
}

type operationJournalV1 struct {
	SchemaVersion int             `json:"schema_version"`
	OperationID   string          `json:"operation_id"`
	Phase         string          `json:"phase"`
	Action        string          `json:"action,omitempty"`
	Host          string          `json:"host,omitempty"`
	MembershipID  string          `json:"membership_id,omitempty"`
	From          HostState       `json:"from,omitempty"`
	To            HostState       `json:"to,omitempty"`
	Target        HostState       `json:"target,omitempty"`
	Result        string          `json:"result,omitempty"`
	OldState      json.RawMessage `json:"old_state,omitempty"`
	NewState      json.RawMessage `json:"new_state"`
	OldConfig     []byte          `json:"old_config,omitempty"`
	NewConfigSHA  string          `json:"new_config_sha256"`
	Reload        bool            `json:"reload"`
	CreatedAt     time.Time       `json:"created_at"`
}

type operationJournalV2 struct {
	SchemaVersion int           `json:"schema_version"`
	OperationID   string        `json:"operation_id"`
	Phase         string        `json:"phase"`
	Action        string        `json:"action,omitempty"`
	Host          string        `json:"host,omitempty"`
	MembershipID  string        `json:"membership_id,omitempty"`
	From          HostState     `json:"from,omitempty"`
	To            HostState     `json:"to,omitempty"`
	Target        HostState     `json:"target,omitempty"`
	Result        string        `json:"result,omitempty"`
	OldState      *State        `json:"old_state,omitempty"`
	NewState      State         `json:"new_state"`
	OldReceipts   *receiptIndex `json:"old_receipts,omitempty"`
	NewReceipts   receiptIndex  `json:"new_receipts"`
	OldConfig     []byte        `json:"old_config,omitempty"`
	NewConfigSHA  string        `json:"new_config_sha256"`
	Reload        bool          `json:"reload"`
	CreatedAt     time.Time     `json:"created_at"`
}

func (c *Controller) decodeOperationJournal(
	data []byte,
) (operationJournal, bool, error) {
	var envelope operationJournalEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return operationJournal{}, false, fmt.Errorf(
			"decode operation journal: %w",
			err,
		)
	}
	switch envelope.SchemaVersion {
	case operationJournalSchemaVersion:
		var journal operationJournal
		if err := json.Unmarshal(data, &journal); err != nil {
			return operationJournal{}, false, fmt.Errorf(
				"decode operation journal: %w",
				err,
			)
		}
		return journal, false, nil
	case rollbackOperationJournalSchemaVersion:
		journal, err := c.migrateOperationJournalV2(data)
		return journal, err == nil, err
	case legacyOperationJournalSchemaVersion:
		journal, err := c.migrateOperationJournalV1(data)
		return journal, err == nil, err
	default:
		return operationJournal{}, false, fmt.Errorf(
			"unsupported operation journal schema %d",
			envelope.SchemaVersion,
		)
	}
}

func (c *Controller) migrateOperationJournalV1(
	data []byte,
) (operationJournal, error) {
	var legacy operationJournalV1
	if err := json.Unmarshal(data, &legacy); err != nil {
		return operationJournal{}, fmt.Errorf(
			"decode operation journal schema 1: %w",
			err,
		)
	}
	newState, err := decodeJournalState(legacy.NewState)
	if err != nil {
		return operationJournal{}, fmt.Errorf(
			"migrate operation journal new state: %w",
			err,
		)
	}
	oldState, err := decodeOptionalJournalState(legacy.OldState)
	if err != nil {
		return operationJournal{}, fmt.Errorf(
			"migrate operation journal old state: %w",
			err,
		)
	}
	receipts, _, err := c.readReceiptIndex()
	if err != nil {
		return operationJournal{}, err
	}
	newReceipts := receipts.Clone()

	membershipID := legacy.MembershipID
	if membershipID == "" {
		if index := newState.hostIndex(legacy.Host); index >= 0 {
			membershipID = newState.Hosts[index].MembershipID
		} else if newState.ActiveTransfer != nil &&
			newState.ActiveTransfer.Host == legacy.Host {
			membershipID = newState.ActiveTransfer.MembershipID
		}
	}
	target := legacy.Target
	if target == "" && newState.ActiveTransfer != nil {
		target = newState.ActiveTransfer.To
	}
	if target == "" && transitionTarget(legacy.To) {
		target = legacy.To
	}
	change := mutation{
		OperationID:  legacy.OperationID,
		Action:       legacy.Action,
		Host:         legacy.Host,
		MembershipID: membershipID,
		From:         legacy.From,
		To:           legacy.To,
		Target:       target,
		Result:       legacy.Result,
	}
	if oldState != nil &&
		oldState.ActiveTransfer != nil &&
		oldState.ActiveTransfer.From == HostJoining &&
		oldState.ActiveTransfer.To == HostActive {
		change.ReceiptAction = "add"
	}
	if deduplicatedMutation(change) && terminalResult(change.Result) {
		if _, exists := newReceipts.Completed[change.OperationID]; !exists {
			if err := newReceipts.Record(
				change.OperationID,
				receiptFromMutation(change),
			); err != nil {
				return operationJournal{}, err
			}
		}
	}
	return c.migrateOperationJournalV2Value(operationJournalV2{
		SchemaVersion: rollbackOperationJournalSchemaVersion,
		OperationID:   legacy.OperationID,
		Phase:         legacy.Phase,
		Action:        legacy.Action,
		Host:          legacy.Host,
		MembershipID:  membershipID,
		From:          legacy.From,
		To:            legacy.To,
		Target:        target,
		Result:        legacy.Result,
		OldState:      oldState,
		NewState:      newState,
		OldReceipts:   &receipts,
		NewReceipts:   newReceipts,
		OldConfig:     legacy.OldConfig,
		NewConfigSHA:  legacy.NewConfigSHA,
		Reload:        legacy.Reload,
		CreatedAt:     legacy.CreatedAt,
	})
}

func (c *Controller) migrateOperationJournalV2(
	data []byte,
) (operationJournal, error) {
	var legacy operationJournalV2
	if err := json.Unmarshal(data, &legacy); err != nil {
		return operationJournal{}, fmt.Errorf(
			"decode operation journal schema 2: %w",
			err,
		)
	}
	return c.migrateOperationJournalV2Value(legacy)
}

func (c *Controller) migrateOperationJournalV2Value(
	legacy operationJournalV2,
) (operationJournal, error) {
	policy := recoveryPolicyRollForward
	phase := operationPhaseIntentCommitted
	var newConfig []byte
	switch legacy.Phase {
	case "prepared", "config_published":
		policy = recoveryPolicyLegacyRollback
		phase = legacy.Phase
	case "reloaded":
		config, err := c.reconstructDesiredConfig(
			legacy.NewState,
			legacy.NewConfigSHA,
		)
		if err != nil {
			return operationJournal{}, err
		}
		newConfig = config
	default:
		return operationJournal{}, fmt.Errorf(
			"migrate operation %s: unknown schema-2 phase %q",
			legacy.OperationID,
			legacy.Phase,
		)
	}
	configSHA := legacy.NewConfigSHA
	if len(newConfig) > 0 {
		configSHA = hashBytes(newConfig)
	}
	change := mutation{
		OperationID:  legacy.OperationID,
		Action:       legacy.Action,
		Host:         legacy.Host,
		MembershipID: legacy.MembershipID,
		From:         legacy.From,
		To:           legacy.To,
		Target:       legacy.Target,
		Result:       legacy.Result,
	}
	return operationJournal{
		SchemaVersion:  operationJournalSchemaVersion,
		OperationID:    legacy.OperationID,
		Phase:          phase,
		Action:         legacy.Action,
		Host:           legacy.Host,
		MembershipID:   legacy.MembershipID,
		From:           legacy.From,
		To:             legacy.To,
		Target:         legacy.Target,
		Result:         legacy.Result,
		OldState:       legacy.OldState,
		NewState:       legacy.NewState,
		OldReceipts:    legacy.OldReceipts,
		NewReceipts:    legacy.NewReceipts,
		OldConfig:      legacy.OldConfig,
		NewConfig:      newConfig,
		NewConfigSHA:   configSHA,
		Audit:          auditRecord(change, legacy.NewState.Generation, configSHA),
		RecoveryPolicy: policy,
		Reload:         legacy.Reload,
		CreatedAt:      legacy.CreatedAt,
	}, nil
}

func (c *Controller) reconstructDesiredConfig(
	state State,
	expectedSHA string,
) ([]byte, error) {
	output, err := os.ReadFile(c.config.OutputPath)
	if err == nil && expectedSHA != "" && hashBytes(output) == expectedSHA {
		return output, nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	template, err := os.ReadFile(c.config.TemplatePath)
	if err != nil {
		return nil, err
	}
	config, err := Render(template, state, c.config.ProxyPolicy)
	if err != nil {
		return nil, err
	}
	return config, nil
}

func decodeJournalState(raw json.RawMessage) (State, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return State{}, fmt.Errorf("operation journal has no new state")
	}
	state, _, err := decodeState(raw)
	return state, err
}

func decodeOptionalJournalState(raw json.RawMessage) (*State, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	state, _, err := decodeState(raw)
	if err != nil {
		return nil, err
	}
	return &state, nil
}
