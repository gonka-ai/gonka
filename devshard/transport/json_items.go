package transport

func countTopLevelJSONItems(data []byte) int {
	items, depth := 0, 0
	inString, escaped := false, false
	seen, topArray, expect := false, false, false
	for _, c := range data {
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		if !seen {
			seen = true
			topArray = c == '['
		}
		if !topArray {
			return 0
		}
		if depth == 1 {
			switch {
			case c == ',':
				expect = true
			case c != ']' && expect:
				items++
				expect = false
			}
		}
		switch c {
		case '"':
			inString = true
		case '{', '[':
			depth++
			if depth == 1 {
				expect = true
			}
		case '}', ']':
			depth--
		}
	}
	return items
}
