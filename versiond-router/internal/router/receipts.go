package router

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"time"
)

const receiptIndexSchemaVersion = 1

type operationReceipt struct {
	Action         string    `json:"action"`
	Host           string    `json:"host"`
	MembershipID   string    `json:"membership_id,omitempty"`
	Target         HostState `json:"target"`
	Result         string    `json:"result"`
	CompletedAt    time.Time `json:"completed_at"`
	ActionAgnostic bool      `json:"action_agnostic,omitempty"`
	Conflict       bool      `json:"conflict,omitempty"`
}

type receiptIndex struct {
	SchemaVersion int                         `json:"schema_version"`
	Completed     map[string]operationReceipt `json:"completed"`
}

func newReceiptIndex() receiptIndex {
	return receiptIndex{
		SchemaVersion: receiptIndexSchemaVersion,
		Completed:     make(map[string]operationReceipt),
	}
}

func (i receiptIndex) Clone() receiptIndex {
	clone := newReceiptIndex()
	for operationID, receipt := range i.Completed {
		clone.Completed[operationID] = receipt
	}
	return clone
}

func (i receiptIndex) Validate() error {
	if i.SchemaVersion != receiptIndexSchemaVersion {
		return fmt.Errorf(
			"unsupported receipt index schema %d",
			i.SchemaVersion,
		)
	}
	if i.Completed == nil {
		return errors.New("receipt index has no completed operation map")
	}
	for operationID, receipt := range i.Completed {
		if err := validateOperationReceipt(operationID, receipt); err != nil {
			return err
		}
	}
	return nil
}

func (i *receiptIndex) Record(operationID string, receipt operationReceipt) error {
	if existing, ok := i.Completed[operationID]; ok {
		if sameOperationReceipt(existing, receipt) {
			return nil
		}
		return fmt.Errorf(
			"%w: operation %s already has a completion receipt",
			ErrOperationOwner,
			operationID,
		)
	}
	i.Completed[operationID] = receipt
	return nil
}

func (c *Controller) loadOrCreateReceiptIndex() (receiptIndex, error) {
	data, err := os.ReadFile(c.config.ReceiptsPath)
	if err == nil {
		var index receiptIndex
		if err := json.Unmarshal(data, &index); err != nil {
			return receiptIndex{}, fmt.Errorf("decode router receipt index: %w", err)
		}
		if err := index.Validate(); err != nil {
			return receiptIndex{}, err
		}
		return index, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return receiptIndex{}, err
	}

	index := newReceiptIndex()
	c.importAuditReceipts(&index)
	if err := writeJSONAtomic(c.config.ReceiptsPath, index, 0o600); err != nil {
		return receiptIndex{}, err
	}
	return index, nil
}

func (c *Controller) importAuditReceipts(index *receiptIndex) {
	f, err := os.Open(c.config.AuditPath)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		slog.Warn("cannot import router audit receipts", "error", err)
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		var record AuditRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			slog.Warn(
				"skipping malformed router audit record",
				"line", line,
				"error", err,
			)
			continue
		}
		if record.OperationID == "" ||
			!terminalAuditRecord(record) ||
			!replayableAuditAction(record.Action) {
			continue
		}
		receipt := receiptFromAudit(record)
		if err := validateOperationReceipt(record.OperationID, receipt); err != nil {
			slog.Warn(
				"skipping invalid router audit receipt",
				"line", line,
				"operation_id", record.OperationID,
				"error", err,
			)
			continue
		}
		if existing, ok := index.Completed[record.OperationID]; ok {
			if sameOperationReceipt(existing, receipt) {
				if receipt.CompletedAt.After(existing.CompletedAt) {
					index.Completed[record.OperationID] = receipt
				}
				continue
			}
			existing.Conflict = true
			index.Completed[record.OperationID] = existing
			continue
		}
		index.Completed[record.OperationID] = receipt
	}
	if err := scanner.Err(); err != nil {
		slog.Warn("router audit receipt import stopped early", "error", err)
	}
}

func receiptFromAudit(record AuditRecord) operationReceipt {
	target := record.Target
	if target == "" && transitionTarget(record.To) {
		target = record.To
	}
	result := record.Result
	if result == "success" {
		result = "completed"
	}
	completedAt := record.Time
	if completedAt.IsZero() {
		completedAt = time.Now().UTC()
	}
	return operationReceipt{
		Action:         record.Action,
		Host:           record.Host,
		MembershipID:   record.MembershipID,
		Target:         target,
		Result:         result,
		CompletedAt:    completedAt,
		ActionAgnostic: true,
	}
}

func receiptFromMutation(change mutation) operationReceipt {
	action := change.ReceiptAction
	if action == "" {
		action = change.Action
	}
	return operationReceipt{
		Action:       action,
		Host:         change.Host,
		MembershipID: change.MembershipID,
		Target:       change.Target,
		Result:       change.Result,
		CompletedAt:  time.Now().UTC(),
	}
}

func validateOperationReceipt(
	operationID string,
	receipt operationReceipt,
) error {
	if err := validateIdentifier("completed operation id", operationID); err != nil {
		return err
	}
	if receipt.Action == "" {
		return fmt.Errorf("completed operation %q has no action", operationID)
	}
	if err := validateIdentifier("completed operation host", receipt.Host); err != nil {
		return err
	}
	if receipt.MembershipID != "" {
		if err := validateIdentifier(
			"completed operation membership id",
			receipt.MembershipID,
		); err != nil {
			return err
		}
	}
	if !transitionTarget(receipt.Target) {
		return fmt.Errorf(
			"completed operation %q has invalid target %q",
			operationID,
			receipt.Target,
		)
	}
	if !terminalResult(receipt.Result) {
		return fmt.Errorf(
			"completed operation %q has non-terminal result %q",
			operationID,
			receipt.Result,
		)
	}
	if receipt.CompletedAt.IsZero() {
		return fmt.Errorf(
			"completed operation %q has no completion time",
			operationID,
		)
	}
	return nil
}

func replayableAuditAction(action string) bool {
	switch action {
	case "transfer", "add", "cancel",
		"drain", "offline", "join", "activate":
		return true
	default:
		return false
	}
}

func terminalAuditRecord(record AuditRecord) bool {
	if terminalResult(record.Result) {
		return true
	}
	if record.Result != "success" {
		return false
	}
	return record.Action == "offline" || record.Action == "activate"
}

func sameOperationReceipt(left, right operationReceipt) bool {
	return left.Action == right.Action &&
		left.Host == right.Host &&
		left.MembershipID == right.MembershipID &&
		left.Target == right.Target &&
		left.Result == right.Result &&
		left.ActionAgnostic == right.ActionAgnostic &&
		left.Conflict == right.Conflict
}
