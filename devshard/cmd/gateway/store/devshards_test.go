package store

import (
	"reflect"
	"strings"
	"testing"
	"unicode"
)

// Test flow:
//  1. Build a `deliberatelyNotUpdated` map naming the columns the upsert intentionally leaves alone: `escrow_id` (the conflict key), `settlement_pending` (moved only by `SetDevshardSettlementPending`), `settle_tx_hash` (written only by `SetDevshardSettleTxHash`, beside its stamp), `route_prefix` (pinned for the escrow's life), and `chain_epoch`, `amount` and `gone_from_chain` (written only by their own statements).
//  2. Iterate every field of `DevshardRecord` via reflection, converting each to its column name via `columnName`.
//  3. Assert each deliberately-excluded column is indeed absent from the upsert's `column = excluded.column` clauses.
//  4. Assert every other column is present in the upsert's update clause, so a re-registration cannot silently keep its old value.
func TestTheDevshardUpsertCarriesEveryFieldItShould(t *testing.T) {
	deliberatelyNotUpdated := map[string]string{
		"escrow_id":          "the conflict key",
		"settlement_pending": "only SetDevshardSettlementPending moves it",
		"settle_tx_hash":     "only SetDevshardSettleTxHash writes it, with the stamp settle_tx_at",
		"route_prefix":       "the version the escrow was bound under never changes",
		"chain_epoch":        "only SetDevshardChainFacts writes what the chain stamped",
		"amount":             "only SetDevshardChainFacts writes what the chain locked",
		"gone_from_chain":    "only MarkDevshardGoneFromChain sets it, with active = 0; an upsert only clears it with active = 1",
	}

	recordType := reflect.TypeFor[DevshardRecord]()
	for index := range recordType.NumField() {
		column := columnName(recordType.Field(index).Name)
		if reason, expected := deliberatelyNotUpdated[column]; expected {
			if strings.Contains(upsertDevshardStatement, column+" = excluded.") {
				t.Fatalf("%s is updated after all, but is documented as left alone: %s", column, reason)
			}
			continue
		}
		if !strings.Contains(upsertDevshardStatement, column+" = excluded."+column) {
			t.Fatalf("DevshardRecord.%s maps to column %q, which the upsert inserts but never updates:"+
				" re-registering an escrow would silently keep its old value",
				recordType.Field(index).Name, column)
		}
	}
}

// columnName turns a Go field name into the snake_case column this schema uses.
func columnName(field string) string {
	runes := []rune(field)
	var column strings.Builder
	for index, symbol := range runes {
		if unicode.IsUpper(symbol) && index > 0 {
			startsWord := unicode.IsLower(runes[index-1])
			endsAcronym := index+1 < len(runes) && unicode.IsLower(runes[index+1])
			if startsWord || endsAcronym {
				column.WriteByte('_')
			}
		}
		column.WriteRune(unicode.ToLower(symbol))
	}
	return column.String()
}

// Test flow:
//  1. Build a table of Go field names, including one with an acronym (`EscrowID`), each paired with its expected snake_case column name.
//  2. Assert `columnName` returns the expected name for each, keeping the acronym whole rather than splitting each letter.
func TestColumnNameKeepsAcronymsWhole(t *testing.T) {
	for field, want := range map[string]string{
		"EscrowID":          "escrow_id",
		"PrivateKeyEnv":     "private_key_env",
		"SettlementPending": "settlement_pending",
		"Active":            "active",
	} {
		if got := columnName(field); got != want {
			t.Errorf("columnName(%q) = %q, want %q", field, got, want)
		}
	}
}
