package accounting

import (
	"reflect"
	"strings"
	"testing"
)

// Test flow:
//  1. Set every bool and string field of an Attempt that CounterKey also names.
//  2. Record the attempt, on one path straight through the book and on the other through a snapshot restored into a new book.
//  3. Finish the nonce and assert each such field reached the counter key, so a field left out of any copy fails here.
func TestEveryRaceFactReachesTheCounterKey(t *testing.T) {
	attempt := Attempt{Nonce: 1, Sent: true, Acknowledged: true, Usage: UsageWinner}
	attemptValue := reflect.ValueOf(&attempt).Elem()
	keyType := reflect.TypeFor[CounterKey]()
	var facts []string
	for index := range attemptValue.NumField() {
		field := attemptValue.Type().Field(index)
		if _, named := keyType.FieldByName(field.Name); !named {
			continue
		}
		switch field.Type.Kind() {
		case reflect.Bool:
			attemptValue.Field(index).SetBool(true)
		case reflect.String:
			attemptValue.Field(index).SetString("set")
		default:
			continue
		}
		facts = append(facts, field.Name)
	}
	if len(facts) == 0 {
		t.Fatal("no field was checked")
	}

	paths := map[string]func(book *Book) *Book{
		"straight": func(book *Book) *Book { return book },
		"restored": func(book *Book) *Book {
			restored := newTestBook(t, 1)
			if err := restored.Restore(book.Snapshot()); err != nil {
				t.Fatalf("Restore(): %v", err)
			}
			return restored
		},
	}
	for name, path := range paths {
		t.Run(name, func(t *testing.T) {
			book := newTestBook(t, 1)
			if err := book.ObserveLatestNonce(testEscrow, 1); err != nil {
				t.Fatalf("ObserveLatestNonce(): %v", err)
			}
			if err := book.RecordRace(testEscrow, []Attempt{attempt}); err != nil {
				t.Fatalf("RecordRace(): %v", err)
			}
			book = path(book)
			markFinished(t, book, testEscrow, 1)

			records := book.Query(QueryFilter{})
			if len(records) != 1 || len(records[0].Counters) != 1 {
				t.Fatalf("got %d records, want one participant holding one bucket", len(records))
			}
			key := reflect.ValueOf(records[0].Counters[0].CounterKey)
			var dropped []string
			for _, fact := range facts {
				if key.FieldByName(fact).IsZero() {
					dropped = append(dropped, fact)
				}
			}
			if len(dropped) > 0 {
				t.Fatalf("facts left out of the counter key: %s", strings.Join(dropped, ", "))
			}
		})
	}
}
