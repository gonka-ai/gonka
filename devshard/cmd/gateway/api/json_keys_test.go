package api

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/store"
)

var snakeCaseKey = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

// Test flow:
//  1. For each of the gateway's response types, varying across store/chain/config records and the local response, entry and envelope types, run assertSnakeCaseKeys on it.
//  2. Assert every exported field's JSON key is snake_case, recursing into nested gateway-owned types.
func TestEveryResponseTypeSpeaksSnakeCase(t *testing.T) {
	for _, payload := range []any{
		store.DevshardRecord{},
		store.RotationStatus{},
		chain.CreateEscrowResult{},
		chain.SettleEscrowResult{},
		config.Overrides{},
		escrowInspectionDetail{},
		inferenceEntry{},
		requestAccountingResponse{},
		modelListResponse{},
		errorEnvelope{},
	} {
		payloadType := reflect.TypeOf(payload)
		t.Run(payloadType.Name(), func(t *testing.T) {
			assertSnakeCaseKeys(t, payloadType, map[reflect.Type]bool{})
		})
	}
}

// Only types this gateway declares are checked: generated and shared types are not ours to retag.
func assertSnakeCaseKeys(t *testing.T, payloadType reflect.Type, seen map[reflect.Type]bool) {
	t.Helper()
	for payloadType.Kind() == reflect.Pointer || payloadType.Kind() == reflect.Slice || payloadType.Kind() == reflect.Map {
		payloadType = payloadType.Elem()
	}
	if payloadType.Kind() != reflect.Struct || seen[payloadType] ||
		!strings.HasPrefix(payloadType.PkgPath(), "devshard/cmd/gateway") {
		return
	}
	seen[payloadType] = true

	for i := range payloadType.NumField() {
		field := payloadType.Field(i)
		if !field.IsExported() {
			continue
		}
		key, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		switch {
		case key == "-":
			continue
		case field.Anonymous && key == "":
		case !snakeCaseKey.MatchString(key):
			t.Errorf("%s.%s serialises as %q, want a snake_case json tag", payloadType.Name(), field.Name, keyOrFieldName(key, field.Name))
		}
		assertSnakeCaseKeys(t, field.Type, seen)
	}
}

func keyOrFieldName(key, fieldName string) string {
	if key == "" {
		return fieldName
	}
	return key
}
