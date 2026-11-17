package accounting

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"
)

const (
	insertEscrow = `INSERT INTO accounting_escrows (escrow_id, model, creation_epoch, latest_nonce, fee_per_nonce, retired)
		 VALUES (?, ?, ?, ?, ?, ?)`
	insertSlot      = `INSERT INTO accounting_slots (escrow_id, slot_id, validator_address) VALUES (?, ?, ?)`
	insertHostStats = `INSERT INTO accounting_host_stats
		 (escrow_id, slot_id, missed, invalid, cost, required_validations, completed_validations)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`
	insertSlotActivity = `INSERT INTO accounting_slot_activity
		 (escrow_id, slot_id, challenged, validations, timeouts_applied, rejected)
		 VALUES (?, ?, ?, ?, ?, ?)`
	insertServiceNonces = `INSERT INTO accounting_service_nonces (escrow_id, slot_id, purpose, count) VALUES (?, ?, ?, ?)`
	insertMoney         = `INSERT INTO accounting_money
		 (escrow_id, slot_id, reserved_cost, actual_cost, refunded_cost, estimated_input, estimated_error,
		  max_tokens, input_tokens, counted_nonces, output_tokens, tokenized_input, tokenized_error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	insertFirstContent = `INSERT INTO accounting_first_content (escrow_id, slot_id, total_milliseconds, attempts)
		 VALUES (?, ?, ?, ?)`
	insertFirstContentBucket = `INSERT INTO accounting_first_content_distribution (escrow_id, bucket, attempts)
		 VALUES (?, ?, ?)`
	insertCounter = `INSERT INTO accounting_counters
		 (escrow_id, slot_id, disposition, ghost_reason, timeout_kind, timeout_action, timeout_reason,
		  terminal, phase, slow_receipt, slow_chunk, clock_drifted, slow_decode, logprobs_decoded, count)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	insertNonce = `INSERT INTO accounting_nonces
		 (escrow_id, nonce, sent, acknowledged, usage, timeout_kind, timeout_action, timeout_reason,
		  terminal, phase, slow_receipt, slow_chunk, clock_drifted, slow_decode, logprobs_decoded)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	insertCost = `INSERT INTO accounting_costs
		 (escrow_id, nonce, reserved, actual, input_length, max_tokens, input, output, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	insertInputCount = `INSERT INTO accounting_input_counts (escrow_id, nonce, tokens, source) VALUES (?, ?, ?, ?)`
)

type rowWriters struct {
	escrow, slot, hostStats, slotActivity, serviceNonces, money, firstContent, firstContentBucket, counter, nonce, cost, inputCount *sql.Stmt
}

// Save writes only the escrows changed since the last save, and removes the ones the book dropped. See README.md, "Storage".
func (s *Store) Save(ctx context.Context, book *Book) (err error) {
	if s == nil || s.db == nil {
		return nil
	}
	s.writing.Lock()
	defer s.writing.Unlock()
	changes := book.takeUnsaved()
	if changes.empty() {
		return nil
	}
	defer func() {
		if err != nil {
			book.requeueUnsaved(changes)
		}
	}()
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning accounting write: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, transaction.Rollback())
		}
	}()

	var writers rowWriters
	for _, target := range []struct {
		statement **sql.Stmt
		query     string
	}{
		{&writers.escrow, insertEscrow},
		{&writers.slot, insertSlot},
		{&writers.hostStats, insertHostStats},
		{&writers.slotActivity, insertSlotActivity},
		{&writers.serviceNonces, insertServiceNonces},
		{&writers.money, insertMoney},
		{&writers.firstContent, insertFirstContent},
		{&writers.firstContentBucket, insertFirstContentBucket},
		{&writers.counter, insertCounter},
		{&writers.nonce, insertNonce},
		{&writers.cost, insertCost},
		{&writers.inputCount, insertInputCount},
	} {
		statement, prepareErr := transaction.PrepareContext(ctx, target.query)
		if prepareErr != nil {
			return fmt.Errorf("preparing an accounting insert: %w", prepareErr)
		}
		defer func() { err = errors.Join(err, statement.Close()) }()
		*target.statement = statement
	}
	updatedAt := changes.snapshot.UpdatedAt.Format(time.RFC3339Nano)
	if _, err = transaction.ExecContext(ctx,
		`INSERT OR REPLACE INTO accounting_meta (key, value) VALUES (?, ?), (?, ?), (?, ?)`,
		metaSchemaVersion, strconv.Itoa(SchemaVersion),
		metaUpdatedAt, updatedAt,
		metaFirstContentDistributionWrittenAt, updatedAt,
	); err != nil {
		return fmt.Errorf("writing accounting meta: %w", err)
	}
	if err = clearStoredRows(ctx, transaction, changes); err != nil {
		return err
	}
	for _, escrow := range changes.snapshot.Escrows {
		if err = writeEscrow(ctx, writers, escrow); err != nil {
			return err
		}
	}
	if err = transaction.Commit(); err != nil {
		return fmt.Errorf("committing accounting write: %w", err)
	}
	return nil
}

func clearStoredRows(ctx context.Context, transaction *sql.Tx, changes unsavedChanges) error {
	if changes.replaceStored {
		for _, table := range escrowTables {
			if _, err := transaction.ExecContext(ctx, "DELETE FROM "+table); err != nil {
				return fmt.Errorf("clearing %s: %w", table, err)
			}
		}
		return nil
	}
	for _, escrowID := range changes.escrowIDs {
		for _, table := range escrowTables {
			if _, err := transaction.ExecContext(ctx, "DELETE FROM "+table+" WHERE escrow_id = ?", escrowID); err != nil {
				return fmt.Errorf("clearing the stored rows of %s: %w", escrowID, err)
			}
		}
	}
	return nil
}

func writeEscrow(ctx context.Context, writers rowWriters, escrow EscrowSnapshot) error {
	identity := escrow.Metadata.EscrowID
	if _, err := writers.escrow.ExecContext(ctx,
		identity, escrow.Metadata.Model, escrow.Metadata.CreationEpoch, escrow.LatestNonce, escrow.FeePerNonce, escrow.Retired,
	); err != nil {
		return fmt.Errorf("writing escrow %s: %w", identity, err)
	}
	for _, slot := range escrow.Metadata.Slots {
		if _, err := writers.slot.ExecContext(ctx, identity, slot.SlotID, slot.ValidatorAddress); err != nil {
			return fmt.Errorf("writing slot %d of %s: %w", slot.SlotID, identity, err)
		}
	}
	for _, slotID := range slices.Sorted(maps.Keys(escrow.HostStats)) {
		stats := escrow.HostStats[slotID]
		if _, err := writers.hostStats.ExecContext(ctx,
			identity, slotID, stats.Missed, stats.Invalid, stats.Cost,
			stats.RequiredValidations, stats.CompletedValidations,
		); err != nil {
			return fmt.Errorf("writing host stats for slot %d of %s: %w", slotID, identity, err)
		}
	}
	for _, slotID := range slices.Sorted(maps.Keys(escrow.SlotActivity)) {
		activity := escrow.SlotActivity[slotID]
		if _, err := writers.slotActivity.ExecContext(ctx,
			identity, slotID, activity.Challenged, activity.Validations,
			activity.TimeoutsApplied, activity.Rejected,
		); err != nil {
			return fmt.Errorf("writing slot activity for slot %d of %s: %w", slotID, identity, err)
		}
	}
	for _, service := range escrow.ServiceNonces {
		if _, err := writers.serviceNonces.ExecContext(ctx, identity, service.SlotID, service.Purpose, service.Count); err != nil {
			return fmt.Errorf("writing service nonces for slot %d of %s: %w", service.SlotID, identity, err)
		}
	}
	for _, slotID := range slices.Sorted(maps.Keys(slotsWithTotals(escrow))) {
		money := escrow.Money[slotID]
		tokenizedInput, tokenizedError := tokenizedColumns(money)
		if _, err := writers.money.ExecContext(ctx,
			identity, slotID, money.Reserved, money.Actual, money.Refunded, money.EstimatedInput,
			money.EstimatedError, money.MaxTokens, money.Input, money.CountedNonces,
			escrow.Produced[slotID], tokenizedInput, tokenizedError,
		); err != nil {
			return fmt.Errorf("writing money for slot %d of %s: %w", slotID, identity, err)
		}
	}
	for _, slotID := range slices.Sorted(maps.Keys(escrow.FirstContent)) {
		timing := escrow.FirstContent[slotID]
		if _, err := writers.firstContent.ExecContext(ctx,
			identity, slotID, timing.FirstContentMillisecondsTotal, timing.FirstContentAttempts,
		); err != nil {
			return fmt.Errorf("writing first-content timing for slot %d of %s: %w", slotID, identity, err)
		}
	}
	for _, bucket := range slices.Sorted(maps.Keys(escrow.FirstContentBuckets)) {
		if _, err := writers.firstContentBucket.ExecContext(ctx, identity, bucket, escrow.FirstContentBuckets[bucket]); err != nil {
			return fmt.Errorf("writing first-content bucket %d of %s: %w", bucket, identity, err)
		}
	}
	for _, counter := range escrow.Counters {
		if _, err := writers.counter.ExecContext(ctx,
			identity, counter.SlotID, counter.Disposition, counter.GhostReason,
			counter.TimeoutKind, counter.TimeoutAction, counter.TimeoutReason,
			counter.Terminal, counter.Phase, counter.SlowReceipt, counter.SlowChunk, counter.ClockDrifted,
			counter.SlowDecode, counter.LogprobsDecoded, counter.Count,
		); err != nil {
			return fmt.Errorf("writing a counter of %s: %w", identity, err)
		}
	}
	for _, stored := range escrow.Nonces {
		if _, err := writers.nonce.ExecContext(ctx,
			identity, stored.Nonce, stored.Sent, stored.Acknowledged, stored.Usage,
			stored.TimeoutKind, stored.TimeoutAction, stored.TimeoutReason,
			stored.Terminal, stored.Phase, stored.SlowReceipt, stored.SlowChunk, stored.ClockDrifted,
			stored.SlowDecode, stored.LogprobsDecoded,
		); err != nil {
			return fmt.Errorf("writing nonce %d of %s: %w", stored.Nonce, identity, err)
		}
	}
	for _, live := range escrow.LiveCosts {
		if _, err := writers.cost.ExecContext(ctx,
			identity, live.Nonce, live.Reserved, live.Actual, live.InputLength, live.MaxTokens,
			live.Input, live.Output, live.Status,
		); err != nil {
			return fmt.Errorf("writing the cost of nonce %d of %s: %w", live.Nonce, identity, err)
		}
	}
	for _, counted := range escrow.InputCounts {
		if _, err := writers.inputCount.ExecContext(ctx, identity, counted.Nonce, counted.Tokens, counted.Source); err != nil {
			return fmt.Errorf("writing the input count of nonce %d of %s: %w", counted.Nonce, identity, err)
		}
	}
	return nil
}

func slotsWithTotals(escrow EscrowSnapshot) map[uint32]struct{} {
	slots := make(map[uint32]struct{}, len(escrow.Money)+len(escrow.Produced))
	for slotID := range escrow.Money {
		slots[slotID] = struct{}{}
	}
	for slotID := range escrow.Produced {
		slots[slotID] = struct{}{}
	}
	return slots
}

func tokenizedColumns(money SlotMoney) (input, errorTokens any) {
	if money.TokenizedShareUnknown {
		return nil, nil
	}
	return money.TokenizedInput, money.TokenizedError
}
