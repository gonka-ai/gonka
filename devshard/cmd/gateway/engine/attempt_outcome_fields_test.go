package engine

import (
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"
	"unsafe"

	"devshard/cmd/gateway/scheduler"
)

// Test flow:
//  1. Set every plain field of an attempt's state that AttemptOutcome also names to a non-zero value, except the chunk heads, which only an empty read reports.
//  2. Build the outcome.
//  3. Assert each such field reached the outcome non-zero, so a field left out of the copy fails here rather than reading as zero downstream.
func TestEveryAttemptFactReachesTheOutcome(t *testing.T) {
	state := attemptState{}
	stateValue := reflect.ValueOf(&state).Elem()
	outcomeType := reflect.TypeFor[AttemptOutcome]()
	var checked []string
	for index := range stateValue.NumField() {
		field := stateValue.Type().Field(index)
		exported := string(unicode.ToUpper(rune(field.Name[0]))) + field.Name[1:]
		if _, named := outcomeType.FieldByName(exported); !named || exported == "FirstChunkHead" || exported == "LastChunkHead" {
			continue
		}
		if setNonZero(reflect.NewAt(field.Type, unsafe.Pointer(stateValue.Field(index).UnsafeAddr())).Elem()) {
			checked = append(checked, exported)
		}
	}

	outcome := reflect.ValueOf(state.outcome(AttemptSpec{Nonce: fixedNonce{}, Now: func() time.Time { return time.Unix(2, 0) }})).Elem()

	if len(checked) == 0 {
		t.Fatal("no field was checked")
	}
	var dropped []string
	for _, name := range checked {
		if outcome.FieldByName(name).IsZero() {
			dropped = append(dropped, name)
		}
	}
	if len(dropped) > 0 {
		t.Fatalf("fields left out of the outcome: %s", strings.Join(dropped, ", "))
	}
}

type fixedNonce struct{ scheduler.Prepared }

func (fixedNonce) Nonce() uint64 { return 1 }

func setNonZero(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.Bool:
		value.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value.SetUint(1)
	case reflect.String:
		value.SetString("set")
	default:
		if value.Type() != reflect.TypeFor[time.Time]() {
			return false
		}
		value.Set(reflect.ValueOf(time.Unix(1, 0)))
	}
	return true
}
