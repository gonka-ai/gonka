package accounting

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// Test flow:
//  1. For each table case, one attempt flagged `SlowDecode` and one flagged `LogprobsDecoded`, build a book.
//  2. Observe the chain's latest nonce and record a race of two attempts on nonces 4 and 8, which share slot 0.
//  3. Save and reload the book.
//  4. Assert the restored book still holds both counters for that slot.
func TestCountersThatDifferOnlyInAFlagBothSurviveARestart(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		attempt Attempt
	}{
		{"slow decode", Attempt{Nonce: 8, Sent: true, Usage: UsageWinner, SlowDecode: true}},
		{"decoded logprobs", Attempt{Nonce: 8, Sent: true, Usage: UsageWinner, LogprobsDecoded: true}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			book := newTestBook(t, 4)
			if err := book.ObserveLatestNonce(testEscrow, 8); err != nil {
				t.Fatalf("ObserveLatestNonce(): %v", err)
			}
			if err := book.RecordRace(testEscrow, []Attempt{
				{Nonce: 4, Sent: true, Usage: UsageWinner},
				testCase.attempt,
			}); err != nil {
				t.Fatalf("RecordRace(): %v", err)
			}
			markFinished(t, book, testEscrow, 4, 8)

			restored := saveAndReload(t, book, openTestStore(t))

			counters := restored.Query(QueryFilter{Participant: participantFor(0)})[0].Counters
			if len(counters) != 2 {
				t.Fatalf("got %d counters after a restart, want both: %+v", len(counters), counters)
			}
		})
	}
}

// Test flow:
//  1. Open a store and query the primary-key columns of the `accounting_counters` table.
//  2. Walk every field of `CounterKey` that has a JSON tag.
//  3. Assert each such field's column name is among the table's primary-key columns.
func TestEveryCounterKeyFieldIsPartOfTheStoredIdentity(t *testing.T) {
	store := openTestStore(t)
	rows, err := store.db.QueryContext(context.Background(), `SELECT name FROM pragma_table_info('accounting_counters') WHERE pk > 0`)
	if err != nil {
		t.Fatalf("reading the counters primary key: %v", err)
	}
	defer rows.Close()
	stored := map[string]bool{}
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatalf("scanning a primary-key column: %v", err)
		}
		stored[column] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the counters primary key: %v", err)
	}

	keyType := reflect.TypeFor[CounterKey]()
	for i := range keyType.NumField() {
		column, _, _ := strings.Cut(keyType.Field(i).Tag.Get("json"), ",")
		if column == "" || column == "-" {
			continue
		}
		if !stored[column] {
			t.Errorf("CounterKey.%s is not in the counters primary key: two counters differing only in it "+
				"would collide and fail the snapshot", keyType.Field(i).Name)
		}
	}
}
