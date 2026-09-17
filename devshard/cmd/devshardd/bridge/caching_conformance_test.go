package bridge

import (
	"reflect"
	"testing"

	"devshard/bridge"
)

// escrowCacheExclusions lists bridge.EscrowInfo fields the warm cache is
// deliberately not required to carry, and why. Any field not listed here
// must survive EscrowCacheFromInfo -> EscrowInfoFromCache unchanged; a field
// added to EscrowInfo without updating either the mappers or this list fails
// TestEscrowCacheRoundTripCarriesEveryField below.
var escrowCacheExclusions = map[string]string{
	"ModelID": "gateway-only routing hint, hosts ignore it at bind",
	"Settled": "settlement state must be read from the chain, never from a warm row",
}

// fillDistinctNonZero sets every field of the struct pointed to by v to a
// distinct non-zero value, by reflection. It never hand-lists field names:
// doing so would recreate the exact bug class (a hand-written field list
// that drifts from the struct) that produced issue #1762.
func fillDistinctNonZero(t *testing.T, v reflect.Value) {
	t.Helper()
	typ := v.Type()
	for i := 0; i < typ.NumField(); i++ {
		field := v.Field(i)
		name := typ.Field(i).Name
		switch field.Kind() {
		case reflect.String:
			field.SetString("val-" + name)
		case reflect.Bool:
			field.SetBool(true)
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			field.SetInt(int64(i) + 1)
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			field.SetUint(uint64(i) + 1)
		case reflect.Slice:
			switch field.Type().Elem().Kind() {
			case reflect.Uint8: // []byte
				field.SetBytes([]byte("bytes-" + name))
			case reflect.String: // []string
				field.Set(reflect.ValueOf([]string{name + "-0", name + "-1"}))
			default:
				t.Fatalf("fillDistinctNonZero: unsupported slice element kind %s for field %s",
					field.Type().Elem().Kind(), name)
			}
		default:
			t.Fatalf("fillDistinctNonZero: unsupported field kind %s for field %s", field.Kind(), name)
		}
	}
}

// TestEscrowCacheRoundTripCarriesEveryField guards against issue #1762: the
// warm escrow cache mappers (EscrowCacheFromInfo / EscrowInfoFromCache) are
// two hand-written field lists, and a field added to bridge.EscrowInfo
// without also being added to both mappers silently drops out of the round
// trip. This test fills every field of EscrowInfo by reflection, round-trips
// it through the cache, and asserts every field survives unless it is in
// escrowCacheExclusions.
func TestEscrowCacheRoundTripCarriesEveryField(t *testing.T) {
	in := &bridge.EscrowInfo{}
	fillDistinctNonZero(t, reflect.ValueOf(in).Elem())

	cache := EscrowCacheFromInfo(in)
	out := EscrowInfoFromCache(&cache)

	inVal := reflect.ValueOf(in).Elem()
	outVal := reflect.ValueOf(out).Elem()
	typ := inVal.Type()

	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		inField := inVal.Field(i).Interface()
		outField := outVal.Field(i).Interface()

		if reason, excluded := escrowCacheExclusions[name]; excluded {
			zero := reflect.Zero(outVal.Field(i).Type()).Interface()
			if !reflect.DeepEqual(outField, zero) {
				t.Errorf("EscrowInfo.%s is in the deliberate-exclusion list (%s) but came back "+
					"non-zero from the warm escrow cache; if it is now intentionally cached, "+
					"remove it from escrowCacheExclusions and add it to EscrowCacheFromInfo / "+
					"EscrowInfoFromCache", name, reason)
			}
			continue
		}

		if !reflect.DeepEqual(inField, outField) {
			t.Errorf("EscrowInfo.%s is not carried through the warm escrow cache: add it to "+
				"EscrowCacheFromInfo and EscrowInfoFromCache, or to the deliberate-exclusion "+
				"list in this test", name)
		}
	}
}
