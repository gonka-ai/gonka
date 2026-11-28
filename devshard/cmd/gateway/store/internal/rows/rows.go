// Package rows holds the Go-side logic both database backends share: affected-row checks, the retry loop and the completion journal's payload rules. It contains no SQL.
package rows

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"

	"devshard/cmd/gateway/store"
	"devshard/user"
)

// RequireOneRow turns a statement that matched no row into store.ErrDevshardNotFound.
func RequireOneRow(result sql.Result, escrowID string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking affected rows for %s: %w", escrowID, err)
	}
	if affected == 0 {
		return fmt.Errorf("%s: %w", escrowID, store.ErrDevshardNotFound)
	}
	return nil
}

// MatchedOneRow reports whether a guarded statement changed the row.
func MatchedOneRow(result sql.Result, escrowID string) (bool, error) {
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("checking affected rows for %s: %w", escrowID, err)
	}
	return affected == 1, nil
}

// CheckCompletionConflict compares a saved entry's payload with the one already stored at its nonce.
func CheckCompletionConflict(escrowID string, entry user.InferenceCompletion, stored []byte) error {
	var previous user.InferenceCompletion
	if err := json.Unmarshal(stored, &previous); err != nil {
		return fmt.Errorf("decoding stored completion %s/%d: %w", escrowID, entry.Nonce, err)
	}
	want, err := json.Marshal(entry.Payload)
	if err != nil {
		return fmt.Errorf("encoding completion %s/%d: %w", escrowID, entry.Nonce, err)
	}
	got, err := json.Marshal(previous.Payload)
	if err != nil {
		return fmt.Errorf("encoding stored completion %s/%d: %w", escrowID, entry.Nonce, err)
	}
	if !bytes.Equal(want, got) {
		return fmt.Errorf("%w: escrow %s nonce %d", user.ErrInferenceCompletionConflict, escrowID, entry.Nonce)
	}
	return nil
}

// DecodeCompletion reads a journal row and refuses one whose nonce or preparation time does not hold.
func DecodeCompletion(escrowID string, nonce uint64, data []byte) (user.InferenceCompletion, error) {
	var entry user.InferenceCompletion
	if err := json.Unmarshal(data, &entry); err != nil {
		return entry, fmt.Errorf("decoding completion %s/%d: %w", escrowID, nonce, err)
	}
	if entry.Nonce != nonce || entry.PreparedAt <= 0 {
		return entry, fmt.Errorf("invalid completion record for escrow %s nonce %d", escrowID, nonce)
	}
	return entry, nil
}
