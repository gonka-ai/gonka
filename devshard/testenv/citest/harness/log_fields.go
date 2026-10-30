package harness

import "strings"

// LogValueKind is how slog encodes a field in JSON stage logs.
type LogValueKind int

const (
	// LogString is a JSON string ("mode":"anchor") or text mode=anchor.
	LogString LogValueKind = iota
	// LogBool is a JSON boolean ("aggregate_spilled":true), not a quoted "true".
	LogBool
	// LogNumber is an unquoted JSON number ("inference_id":8).
	LogNumber
)

// LogField is one key/value to find on a compose log line.
type LogField struct {
	Key string
	// Value is the decimal or literal text. For a number prefix (delta of a
	// negative height) it is the leading text, such as "-".
	Value  string
	Kind   LogValueKind
	Prefix bool
}

func LogStringField(key, value string) LogField {
	return LogField{Key: key, Value: value, Kind: LogString}
}

func LogBoolField(key string, value bool) LogField {
	text := "false"
	if value {
		text = "true"
	}
	return LogField{Key: key, Value: text, Kind: LogBool}
}

func LogNumberField(key, decimal string) LogField {
	return LogField{Key: key, Value: decimal, Kind: LogNumber}
}

// LogNumberPrefixField matches a number that starts with prefix, such as "-"
// for any negative delta.
func LogNumberPrefixField(key, prefix string) LogField {
	return LogField{Key: key, Value: prefix, Kind: LogNumber, Prefix: true}
}

// LogsContainFields reports whether one log line carries every field, either
// as a slog JSON attr or as text key=value.
func LogsContainFields(logs string, fields ...LogField) bool {
	if len(fields) == 0 {
		return false
	}
	for _, line := range strings.Split(logs, "\n") {
		if lineHasFields(line, fields) {
			return true
		}
	}
	return false
}

func lineHasFields(line string, fields []LogField) bool {
	for _, field := range fields {
		if !lineHasField(line, field) {
			return false
		}
	}
	return true
}

func lineHasField(line string, field LogField) bool {
	switch field.Kind {
	case LogBool, LogNumber:
		return hasRawField(line, field.Key, field.Value, field.Prefix, field.Kind == LogNumber)
	default:
		return hasStringField(line, field.Key, field.Value)
	}
}

func hasStringField(line, key, value string) bool {
	if strings.Contains(line, `"`+key+`":"`+value+`"`) || strings.Contains(line, `"`+key+`": "`+value+`"`) {
		return true
	}
	return hasTextField(line, key, value, func(b byte) bool { return !isIdent(b) })
}

func hasRawField(line, key, raw string, prefix, number bool) bool {
	bound := func(b byte) bool { return !isIdent(b) }
	if number {
		bound = func(b byte) bool { return b < '0' || b > '9' }
	}
	if hasJSONRaw(line, key, raw, prefix, bound) {
		return true
	}
	return hasTextField(line, key, raw, func(b byte) bool {
		if prefix {
			return true
		}
		return bound(b)
	})
}

func hasJSONRaw(line, key, raw string, prefix bool, bound func(byte) bool) bool {
	for _, sep := range []string{`":`, `": `} {
		needle := `"` + key + sep
		rest := line
		for {
			i := strings.Index(rest, needle)
			if i < 0 {
				break
			}
			after := rest[i+len(needle):]
			if strings.HasPrefix(after, raw) && rawBounded(after, len(raw), prefix, bound) {
				return true
			}
			rest = rest[i+len(needle):]
		}
	}
	return false
}

func hasTextField(line, key, raw string, bound func(byte) bool) bool {
	needle := key + "="
	rest := line
	for {
		i := strings.Index(rest, needle)
		if i < 0 {
			return false
		}
		abs := len(line) - len(rest) + i
		if (abs == 0 || !isIdent(line[abs-1])) && strings.HasPrefix(rest[i+len(needle):], raw) {
			after := rest[i+len(needle):]
			if rawBounded(after, len(raw), false, bound) {
				return true
			}
		}
		if i+1 >= len(rest) {
			return false
		}
		rest = rest[i+1:]
	}
}

func rawBounded(after string, n int, prefix bool, bound func(byte) bool) bool {
	if prefix {
		return true
	}
	if n == len(after) {
		return true
	}
	return bound(after[n])
}

func isIdent(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}
