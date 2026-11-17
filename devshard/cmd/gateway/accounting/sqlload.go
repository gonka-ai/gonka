package accounting

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"devshard/types"
)

func (s *Store) Load(ctx context.Context) (Snapshot, error) {
	snapshot := Snapshot{SchemaVersion: SchemaVersion}
	if s == nil || s.db == nil {
		return snapshot, nil
	}
	storedVersion, updatedAt, err := s.readMeta(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	if storedVersion == 0 {
		return snapshot, nil
	}
	if storedVersion != SchemaVersion {
		return Snapshot{}, fmt.Errorf("accounting: stored schema %d is not %d", storedVersion, SchemaVersion)
	}
	snapshot.UpdatedAt = updatedAt

	escrows, order, err := s.readEscrows(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	for _, read := range []func(context.Context, map[string]*EscrowSnapshot) error{
		s.readSlots, s.readHostStats, s.readSlotActivity, s.readServiceNonces, s.readMoney, s.readFirstContent,
		s.readFirstContentBuckets, s.readCounters, s.readNonces, s.readCosts, s.readInputCounts,
	} {
		if err := read(ctx, escrows); err != nil {
			return Snapshot{}, err
		}
	}
	for _, escrowID := range order {
		snapshot.Escrows = append(snapshot.Escrows, *escrows[escrowID])
	}
	return snapshot, nil
}

func (s *Store) readMeta(ctx context.Context) (int, time.Time, error) {
	var version int
	var updatedAt time.Time
	err := s.eachRow(ctx, `SELECT key, value FROM accounting_meta`, "accounting meta",
		func(rows *sql.Rows) error {
			var key, value string
			if err := rows.Scan(&key, &value); err != nil {
				return err
			}
			var parseErr error
			switch key {
			case metaSchemaVersion:
				version, parseErr = strconv.Atoi(value)
			case metaUpdatedAt:
				updatedAt, parseErr = time.Parse(time.RFC3339Nano, value)
			}
			if parseErr != nil {
				return fmt.Errorf("%s is malformed: %w", key, parseErr)
			}
			return nil
		})
	return version, updatedAt, err
}

func (s *Store) readEscrows(ctx context.Context) (map[string]*EscrowSnapshot, []string, error) {
	escrows := make(map[string]*EscrowSnapshot)
	var order []string
	err := s.eachRow(ctx,
		`SELECT escrow_id, model, creation_epoch, latest_nonce, fee_per_nonce, retired
		 FROM accounting_escrows ORDER BY escrow_id`, "escrows",
		func(rows *sql.Rows) error {
			var stored EscrowSnapshot
			if err := rows.Scan(&stored.Metadata.EscrowID, &stored.Metadata.Model,
				&stored.Metadata.CreationEpoch, &stored.LatestNonce, &stored.FeePerNonce, &stored.Retired); err != nil {
				return err
			}
			stored.HostStats = make(map[uint32]types.HostStats)
			stored.SlotActivity = make(map[uint32]SlotActivity)
			stored.Money = make(map[uint32]SlotMoney)
			stored.Produced = make(map[uint32]uint64)
			stored.FirstContent = make(map[uint32]FirstContentTiming)
			stored.FirstContentBuckets = make(FirstContentBuckets)
			escrows[stored.Metadata.EscrowID] = &stored
			order = append(order, stored.Metadata.EscrowID)
			return nil
		})
	return escrows, order, err
}

func (s *Store) readSlots(ctx context.Context, escrows map[string]*EscrowSnapshot) error {
	return s.eachRow(ctx,
		`SELECT escrow_id, slot_id, validator_address FROM accounting_slots ORDER BY escrow_id, slot_id`,
		"slots",
		func(rows *sql.Rows) error {
			var escrowID string
			var slot types.SlotAssignment
			if err := rows.Scan(&escrowID, &slot.SlotID, &slot.ValidatorAddress); err != nil {
				return err
			}
			if escrow, known := escrows[escrowID]; known {
				escrow.Metadata.Slots = append(escrow.Metadata.Slots, slot)
			}
			return nil
		})
}

func (s *Store) readHostStats(ctx context.Context, escrows map[string]*EscrowSnapshot) error {
	return s.eachRow(ctx,
		`SELECT escrow_id, slot_id, missed, invalid, cost, required_validations, completed_validations
		 FROM accounting_host_stats`, "host stats",
		func(rows *sql.Rows) error {
			var escrowID string
			var slotID uint32
			var stats types.HostStats
			if err := rows.Scan(&escrowID, &slotID, &stats.Missed, &stats.Invalid, &stats.Cost,
				&stats.RequiredValidations, &stats.CompletedValidations); err != nil {
				return err
			}
			if escrow, known := escrows[escrowID]; known {
				escrow.HostStats[slotID] = stats
			}
			return nil
		})
}

func (s *Store) readSlotActivity(ctx context.Context, escrows map[string]*EscrowSnapshot) error {
	return s.eachRow(ctx,
		`SELECT escrow_id, slot_id, challenged, validations, timeouts_applied, rejected
		 FROM accounting_slot_activity`, "slot activity",
		func(rows *sql.Rows) error {
			var escrowID string
			var slotID uint32
			var activity SlotActivity
			if err := rows.Scan(&escrowID, &slotID, &activity.Challenged, &activity.Validations,
				&activity.TimeoutsApplied, &activity.Rejected); err != nil {
				return err
			}
			if escrow, known := escrows[escrowID]; known {
				escrow.SlotActivity[slotID] = activity
			}
			return nil
		})
}

func (s *Store) readServiceNonces(ctx context.Context, escrows map[string]*EscrowSnapshot) error {
	return s.eachRow(ctx,
		`SELECT escrow_id, slot_id, purpose, count FROM accounting_service_nonces ORDER BY escrow_id, slot_id, purpose`,
		"service nonces",
		func(rows *sql.Rows) error {
			var escrowID string
			var service PersistedServiceCount
			if err := rows.Scan(&escrowID, &service.SlotID, &service.Purpose, &service.Count); err != nil {
				return err
			}
			if escrow, known := escrows[escrowID]; known {
				escrow.ServiceNonces = append(escrow.ServiceNonces, service)
			}
			return nil
		})
}

func (s *Store) readMoney(ctx context.Context, escrows map[string]*EscrowSnapshot) error {
	return s.eachRow(ctx,
		`SELECT escrow_id, slot_id, reserved_cost, actual_cost, refunded_cost, estimated_input, estimated_error,
		        max_tokens, input_tokens, counted_nonces, output_tokens, tokenized_input, tokenized_error
		 FROM accounting_money`, "money",
		func(rows *sql.Rows) error {
			var escrowID string
			var slotID uint32
			var money SlotMoney
			var produced uint64
			var tokenizedInput, tokenizedError sql.Null[uint64]
			if err := rows.Scan(&escrowID, &slotID, &money.Reserved, &money.Actual, &money.Refunded,
				&money.EstimatedInput, &money.EstimatedError, &money.MaxTokens,
				&money.Input, &money.CountedNonces, &produced, &tokenizedInput, &tokenizedError); err != nil {
				return err
			}
			money.TokenizedInput, money.TokenizedError = tokenizedInput.V, tokenizedError.V
			money.TokenizedShareUnknown = (!tokenizedInput.Valid || !tokenizedError.Valid) && money.EstimatedInput+money.EstimatedError > 0
			if escrow, known := escrows[escrowID]; known {
				if money != (SlotMoney{}) {
					escrow.Money[slotID] = money
				}
				if produced > 0 {
					escrow.Produced[slotID] = produced
				}
			}
			return nil
		})
}

func (s *Store) readFirstContent(ctx context.Context, escrows map[string]*EscrowSnapshot) error {
	return s.eachRow(ctx,
		`SELECT escrow_id, slot_id, total_milliseconds, attempts FROM accounting_first_content`, "first-content timing",
		func(rows *sql.Rows) error {
			var escrowID string
			var slotID uint32
			var timing FirstContentTiming
			if err := rows.Scan(&escrowID, &slotID, &timing.FirstContentMillisecondsTotal, &timing.FirstContentAttempts); err != nil {
				return err
			}
			if escrow, known := escrows[escrowID]; known {
				escrow.FirstContent[slotID] = timing
			}
			return nil
		})
}

func (s *Store) readFirstContentBuckets(ctx context.Context, escrows map[string]*EscrowSnapshot) error {
	return s.eachRow(ctx,
		`SELECT escrow_id, bucket, attempts FROM accounting_first_content_distribution`, "first-content buckets",
		func(rows *sql.Rows) error {
			var escrowID string
			var bucket, attempts uint64
			if err := rows.Scan(&escrowID, &bucket, &attempts); err != nil {
				return err
			}
			if escrow, known := escrows[escrowID]; known && bucket <= uint64(firstContentOverflow) {
				escrow.FirstContentBuckets[FirstContentBucket(bucket)] = attempts
			}
			return nil
		})
}

func (s *Store) readCounters(ctx context.Context, escrows map[string]*EscrowSnapshot) error {
	return s.eachRow(ctx,
		`SELECT escrow_id, slot_id, disposition, ghost_reason, timeout_kind, timeout_action, timeout_reason,
		        terminal, phase, slow_receipt, slow_chunk, clock_drifted, slow_decode, logprobs_decoded, count
		 FROM accounting_counters ORDER BY escrow_id, slot_id, disposition`, "counters",
		func(rows *sql.Rows) error {
			var escrowID string
			var counter PersistedCounter
			if err := rows.Scan(&escrowID, &counter.SlotID, &counter.Disposition, &counter.GhostReason,
				&counter.TimeoutKind, &counter.TimeoutAction, &counter.TimeoutReason,
				&counter.Terminal, &counter.Phase, &counter.SlowReceipt, &counter.SlowChunk, &counter.ClockDrifted,
				&counter.SlowDecode, &counter.LogprobsDecoded, &counter.Count); err != nil {
				return err
			}
			if escrow, known := escrows[escrowID]; known {
				escrow.Counters = append(escrow.Counters, counter)
			}
			return nil
		})
}

func (s *Store) readNonces(ctx context.Context, escrows map[string]*EscrowSnapshot) error {
	return s.eachRow(ctx,
		`SELECT escrow_id, nonce, sent, acknowledged, usage, timeout_kind, timeout_action, timeout_reason,
		        terminal, phase, slow_receipt, slow_chunk, clock_drifted, slow_decode, logprobs_decoded
		 FROM accounting_nonces ORDER BY escrow_id, nonce`, "nonces",
		func(rows *sql.Rows) error {
			var escrowID string
			var stored PersistedNonce
			if err := rows.Scan(&escrowID, &stored.Nonce, &stored.Sent, &stored.Acknowledged, &stored.Usage,
				&stored.TimeoutKind, &stored.TimeoutAction, &stored.TimeoutReason,
				&stored.Terminal, &stored.Phase, &stored.SlowReceipt, &stored.SlowChunk, &stored.ClockDrifted,
				&stored.SlowDecode, &stored.LogprobsDecoded); err != nil {
				return err
			}
			if escrow, known := escrows[escrowID]; known {
				escrow.Nonces = append(escrow.Nonces, stored)
			}
			return nil
		})
}

func (s *Store) eachRow(ctx context.Context, query, what string, scan func(*sql.Rows) error) error {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("reading %s: %w", what, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return fmt.Errorf("reading %s: %w", what, err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("reading %s: %w", what, err)
	}
	return nil
}

func (s *Store) readCosts(ctx context.Context, escrows map[string]*EscrowSnapshot) error {
	return s.eachRow(ctx,
		`SELECT escrow_id, nonce, reserved, actual, input_length, max_tokens, input, output, status
		 FROM accounting_costs ORDER BY escrow_id, nonce`, "costs",
		func(rows *sql.Rows) error {
			var escrowID string
			var live PersistedCost
			if err := rows.Scan(&escrowID, &live.Nonce, &live.Reserved, &live.Actual, &live.InputLength,
				&live.MaxTokens, &live.Input, &live.Output, &live.Status); err != nil {
				return err
			}
			if escrow, known := escrows[escrowID]; known {
				escrow.LiveCosts = append(escrow.LiveCosts, live)
			}
			return nil
		})
}

func (s *Store) readInputCounts(ctx context.Context, escrows map[string]*EscrowSnapshot) error {
	return s.eachRow(ctx,
		`SELECT escrow_id, nonce, tokens, source FROM accounting_input_counts ORDER BY escrow_id, nonce`, "input counts",
		func(rows *sql.Rows) error {
			var escrowID string
			var counted PersistedInputCount
			if err := rows.Scan(&escrowID, &counted.Nonce, &counted.Tokens, &counted.Source); err != nil {
				return err
			}
			if escrow, known := escrows[escrowID]; known {
				escrow.InputCounts = append(escrow.InputCounts, counted)
			}
			return nil
		})
}
