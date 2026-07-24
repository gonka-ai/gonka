package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sort"
)

const auditOutboxSchemaVersion = 1

type auditOutbox struct {
	SchemaVersion int                    `json:"schema_version"`
	Pending       map[string]AuditRecord `json:"pending"`
}

func newAuditOutbox() auditOutbox {
	return auditOutbox{
		SchemaVersion: auditOutboxSchemaVersion,
		Pending:       make(map[string]AuditRecord),
	}
}

func (c *Controller) enqueueAudit(record AuditRecord) error {
	if record.EventID == "" {
		return errors.New("router audit record has no event id")
	}
	outbox, err := c.loadAuditOutbox()
	if err != nil {
		return err
	}
	if existing, ok := outbox.Pending[record.EventID]; ok {
		if sameAuditRecord(existing, record) {
			return nil
		}
		return fmt.Errorf(
			"router audit event %q has conflicting data",
			record.EventID,
		)
	}
	outbox.Pending[record.EventID] = record
	return writeJSONAtomic(c.config.AuditOutboxPath, outbox, 0o600)
}

func (c *Controller) flushAuditOutbox() error {
	outbox, err := c.loadAuditOutbox()
	if err != nil {
		return err
	}
	eventIDs := make([]string, 0, len(outbox.Pending))
	for eventID := range outbox.Pending {
		eventIDs = append(eventIDs, eventID)
	}
	sort.Slice(eventIDs, func(i, j int) bool {
		left := outbox.Pending[eventIDs[i]]
		right := outbox.Pending[eventIDs[j]]
		if left.Time.Equal(right.Time) {
			return eventIDs[i] < eventIDs[j]
		}
		return left.Time.Before(right.Time)
	})
	for _, eventID := range eventIDs {
		if err := c.appendAudit(outbox.Pending[eventID]); err != nil {
			return err
		}
		delete(outbox.Pending, eventID)
		if err := writeJSONAtomic(
			c.config.AuditOutboxPath,
			outbox,
			0o600,
		); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) flushAuditBestEffort() {
	if err := c.flushAuditOutbox(); err != nil {
		slog.Warn(
			"router audit delivery is pending",
			"outbox", c.config.AuditOutboxPath,
			"error", err,
		)
	}
}

func (c *Controller) recordAuditBestEffort(record AuditRecord) {
	if err := c.appendAudit(record); err != nil {
		slog.Warn(
			"cannot append router audit record",
			"operation_id", record.OperationID,
			"result", record.Result,
			"error", err,
		)
	}
}

func (c *Controller) loadAuditOutbox() (auditOutbox, error) {
	data, err := os.ReadFile(c.config.AuditOutboxPath)
	if errors.Is(err, fs.ErrNotExist) {
		return newAuditOutbox(), nil
	}
	if err != nil {
		return auditOutbox{}, err
	}
	var outbox auditOutbox
	if err := json.Unmarshal(data, &outbox); err != nil {
		return auditOutbox{}, fmt.Errorf("decode router audit outbox: %w", err)
	}
	if outbox.SchemaVersion != auditOutboxSchemaVersion {
		return auditOutbox{}, fmt.Errorf(
			"unsupported audit outbox schema %d",
			outbox.SchemaVersion,
		)
	}
	if outbox.Pending == nil {
		return auditOutbox{}, errors.New("router audit outbox has no pending map")
	}
	return outbox, nil
}

func sameAuditRecord(left, right AuditRecord) bool {
	left.Time = right.Time
	return left == right
}
