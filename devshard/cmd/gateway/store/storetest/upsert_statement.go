package storetest

import (
	"reflect"
	"strings"
	"testing"
	"unicode"

	"devshard/cmd/gateway/store"
)

// CheckUpsertStatement fails t when the upsert leaves a column out of its update, or updates one that only its own statement may write.
func CheckUpsertStatement(t *testing.T, statement string) {
	deliberatelyNotUpdated := map[string]string{
		"escrow_id":          "the conflict key",
		"settlement_pending": "only SetDevshardSettlementPending moves it",
		"settle_tx_hash":     "only SetDevshardSettleTxHash writes it, with the stamp settle_tx_at",
		"route_prefix":       "the version the escrow was bound under never changes",
		"chain_epoch":        "only SetDevshardChainFacts writes what the chain stamped",
		"amount":             "only SetDevshardChainFacts writes what the chain locked",
		"gone_from_chain":    "only MarkDevshardGoneFromChain sets it, with active = 0; an upsert only clears it with active = 1",
	}
	recordType := reflect.TypeFor[store.DevshardRecord]()
	for index := range recordType.NumField() {
		column := columnName(recordType.Field(index).Name)
		if reason, expected := deliberatelyNotUpdated[column]; expected {
			if strings.Contains(statement, column+" = excluded.") {
				t.Fatalf("%s is updated after all, but is documented as left alone: %s", column, reason)
			}
			continue
		}
		if !strings.Contains(statement, column+" = excluded."+column) {
			t.Fatalf("DevshardRecord.%s maps to column %q, which the upsert inserts but never updates: re-registering an escrow would silently keep its old value",
				recordType.Field(index).Name, column)
		}
	}
}

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
