package config

import (
	"reflect"
	"testing"

	"devshard/cmd/gateway/env"
)

// Test flow:
//  1. Walk every field of env.Tunables and set only that one, to two different values.
//  2. Apply each to the defaults.
//  3. Assert the two configs differ, so a field with no line in applyTunables fails here.
func TestEveryTunableReachesTheConfig(t *testing.T) {
	tunablesType := reflect.TypeFor[env.Tunables]()
	for index := range tunablesType.NumField() {
		field := tunablesType.Field(index)
		t.Run(field.Name, func(t *testing.T) {
			if reflect.DeepEqual(appliedWith(t, index, field.Type, 0), appliedWith(t, index, field.Type, 1)) {
				t.Fatalf("%s reached no config field", field.Name)
			}
		})
	}
}

func appliedWith(t *testing.T, index int, pointerType reflect.Type, choice int) Config {
	t.Helper()
	value := reflect.New(pointerType.Elem())
	switch value.Elem().Kind() {
	case reflect.Int64:
		value.Elem().SetInt(int64(choice + 1))
	case reflect.Float64:
		value.Elem().SetFloat(float64(choice + 1))
	case reflect.String:
		value.Elem().SetString(string(rune('a' + choice)))
	case reflect.Bool:
		value.Elem().SetBool(choice == 1)
	default:
		t.Fatalf("no value for a tunable of kind %s", value.Elem().Kind())
	}
	tunables := env.Tunables{}
	reflect.ValueOf(&tunables).Elem().Field(index).Set(value)
	configuration := Defaults()
	applyTunables(&configuration, tunables)
	return configuration
}
