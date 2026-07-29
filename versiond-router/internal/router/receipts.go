package router

import (
	"bytes"
	"context"
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
	Action       string    `json:"action"`
	Host         string    `json:"host"`
	MembershipID string    `json:"membership_id,omitempty"`
	Target       HostState `json:"target"`
	Result       string    `json:"result"`
	CompletedAt  time.Time `json:"completed_at"`
}

type receiptIndex struct {
	SchemaVersion int                         `json:"schema_version"`
	Completed     map[string]operationReceipt `json:"completed"`
}

// OperationCompletion is the durable terminal result for one operation ID.
type OperationCompletion struct {
	OperationID  string    `json:"operation_id"`
	Action       string    `json:"action"`
	Host         string    `json:"host"`
	MembershipID string    `json:"membership_id,omitempty"`
	Target       HostState `json:"target"`
	Result       string    `json:"result"`
	CompletedAt  time.Time `json:"completed_at"`
}

// OperationLookup reports whether one operation ID has a completion receipt.
type OperationLookup struct {
	OperationID string               `json:"operation_id"`
	Completed   bool                 `json:"completed"`
	Completion  *OperationCompletion `json:"completion,omitempty"`
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
	index, missing, err := c.readReceiptIndex()
	if err != nil {
		return receiptIndex{}, err
	}
	if !missing {
		return index, nil
	}
	if err := writeJSONAtomic(c.config.ReceiptsPath, index, 0o600); err != nil {
		return receiptIndex{}, err
	}
	return index, nil
}

func (c *Controller) persistCompletionReceipt(
	operationID string,
	receipt operationReceipt,
) error {
	index, err := c.loadOrCreateReceiptIndex()
	if err != nil {
		return err
	}
	if err := index.Record(operationID, receipt); err != nil {
		return err
	}
	if err := index.Validate(); err != nil {
		return err
	}
	return writeJSONAtomic(c.config.ReceiptsPath, index, 0o600)
}

// LookupOperation reads one completion receipt without mutating router state.
func (c *Controller) LookupOperation(
	ctx context.Context,
	operationID string,
) (OperationLookup, error) {
	result := OperationLookup{OperationID: operationID}
	if err := validateIdentifier("operation id", operationID); err != nil {
		return result, err
	}
	err := c.withLock(ctx, func() error {
		index, _, err := c.readReceiptIndex()
		if err != nil {
			return err
		}
		receipt, ok := index.Completed[operationID]
		if !ok {
			return nil
		}
		result.Completed = true
		result.Completion = &OperationCompletion{
			OperationID:  operationID,
			Action:       receipt.Action,
			Host:         receipt.Host,
			MembershipID: receipt.MembershipID,
			Target:       receipt.Target,
			Result:       receipt.Result,
			CompletedAt:  receipt.CompletedAt,
		}
		return nil
	})
	return result, err
}

func (c *Controller) readReceiptIndex() (receiptIndex, bool, error) {
	data, err := os.ReadFile(c.config.ReceiptsPath)
	if err == nil {
		var index receiptIndex
		if err := json.Unmarshal(data, &index); err != nil {
			return receiptIndex{}, false, fmt.Errorf(
				"decode router receipt index: %w",
				err,
			)
		}
		if err := index.Validate(); err != nil {
			return receiptIndex{}, false, err
		}
		return index, false, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return receiptIndex{}, false, err
	}

	return newReceiptIndex(), true, nil
}

func (c *Controller) repairTornAuditTail() error {
	f, err := os.OpenFile(c.config.AuditPath, os.O_RDWR, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open router audit for tail repair: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("inspect router audit for tail repair: %w", err)
	}
	if info.Size() == 0 {
		return nil
	}
	var last [1]byte
	if _, err := f.ReadAt(last[:], info.Size()-1); err != nil {
		return fmt.Errorf("read router audit tail: %w", err)
	}
	if last[0] == '\n' {
		return nil
	}

	const chunkSize int64 = 64 * 1024
	truncateAt := int64(0)
	for end := info.Size(); end > 0; {
		start := max(int64(0), end-chunkSize)
		chunk := make([]byte, end-start)
		if _, err := f.ReadAt(chunk, start); err != nil {
			return fmt.Errorf("scan router audit tail: %w", err)
		}
		if newline := bytes.LastIndexByte(chunk, '\n'); newline >= 0 {
			truncateAt = start + int64(newline) + 1
			break
		}
		end = start
	}
	if err := f.Truncate(truncateAt); err != nil {
		return fmt.Errorf("truncate torn router audit tail: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync repaired router audit: %w", err)
	}
	slog.Warn(
		"discarded uncommitted torn router audit tail",
		"path", c.config.AuditPath,
		"bytes", info.Size()-truncateAt,
	)
	return nil
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

func sameOperationReceipt(left, right operationReceipt) bool {
	return left.Action == right.Action &&
		left.Host == right.Host &&
		left.MembershipID == right.MembershipID &&
		left.Target == right.Target &&
		left.Result == right.Result
}
