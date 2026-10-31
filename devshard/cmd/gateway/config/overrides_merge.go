package config

import (
	"fmt"
	"reflect"
	"strings"

	json "github.com/goccy/go-json"
)

const jsonTagSeparator = ","

// Merged applies a settings patch: a field it names replaces the stored one, a field it sets to null clears it, and every other field stays. See ../docs/operations.md, "Configuration".
func (o Overrides) Merged(patch []byte) (Overrides, error) {
	changes, err := ParseOverrides(patch)
	if err != nil {
		return Overrides{}, err
	}
	var named map[string]json.RawMessage
	if err := json.Unmarshal(patch, &named); err != nil {
		return Overrides{}, fmt.Errorf("parsing overrides patch: %w", err)
	}
	merged := o
	target, source := reflect.ValueOf(&merged).Elem(), reflect.ValueOf(changes)
	fields := target.Type()
	for index := range fields.NumField() {
		name, _, _ := strings.Cut(fields.Field(index).Tag.Get("json"), jsonTagSeparator)
		if _, present := named[name]; present {
			target.Field(index).Set(source.Field(index))
		}
	}
	return merged, nil
}
