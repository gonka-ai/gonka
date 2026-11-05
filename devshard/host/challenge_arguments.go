package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// renderTurn closes a final string argument only in the copy sent to the
// renderer. A non-string cut is still governed by the request's skip rule.
func (a servedAnswer) renderTurn(terms executedTerms) (map[string]any, error) {
	turn := a.turn()
	call, cut := a.cutCall()
	if !cut || !terms.tools {
		return turn, nil
	}
	argument, ok := openArgument(call.arguments)
	if !ok || !terms.stringArgument(call.name, argument) {
		return turn, nil
	}
	arguments, err := repairStringArguments(call.arguments)
	if err != nil {
		return nil, fmt.Errorf("repair tool arguments: %w", err)
	}
	calls := turn["tool_calls"].([]any)
	function := calls[len(calls)-1].(map[string]any)["function"].(map[string]any)
	function["arguments"] = arguments
	return turn, nil
}

// repairStringArguments applies partial_json_parser's open-string completion:
// discard an unfinished escape and close the string and its enclosing object.
// Trailing whitespace inside the string is kept. Completed fields retain their
// original spelling and order; vLLM parses arguments before applying its template.
func repairStringArguments(arguments string) (string, error) {
	dec := json.NewDecoder(strings.NewReader(arguments))
	if token, err := dec.Token(); err != nil || token != json.Delim('{') {
		return "", errors.New("arguments are not an object")
	}
	for {
		key, err := dec.Token()
		if err != nil {
			return "", errors.New("no open string argument")
		}
		if _, ok := key.(string); !ok {
			return "", errors.New("no open string argument")
		}
		start := int(dec.InputOffset())
		var value json.RawMessage
		if dec.Decode(&value) == nil {
			continue
		}
		// InputOffset is just after the key; only JSON whitespace and the
		// colon may separate it from the unfinished value.
		tail := strings.TrimLeft(arguments[start:], " \t\r\n")
		if !strings.HasPrefix(tail, ":") {
			return "", errors.New("argument has no colon")
		}
		tail = strings.TrimLeft(tail[1:], " \t\r\n")
		if !strings.HasPrefix(tail, `"`) {
			return "", errors.New("incomplete argument is not an open string")
		}
		start = len(arguments) - len(tail)
		end := len(arguments)
		for i := start + 1; i < len(arguments); i++ {
			switch arguments[i] {
			case '"':
				return "", errors.New("incomplete argument is not an open string")
			case '\\':
				if i+1 == len(arguments) {
					end = i
					break
				}
				i++
			}
		}
		// The partial parser drops incomplete escape tails in this order;
		// complete but invalid escapes are rejected by the JSON parser below.
		for _, escape := range []struct {
			prefix string
			width  int
		}{{`\u`, 5}, {`\U`, 9}, {`\x`, 3}} {
			from := end
			for n := 0; n < escape.width && from > start; n++ {
				_, size := utf8.DecodeLastRuneInString(arguments[:from])
				from -= size
			}
			i := strings.LastIndex(arguments[from:end], escape.prefix)
			if i < 0 {
				continue
			}
			i += from
			slashes := 0
			for j := i - 1; j > start && arguments[j] == '\\'; j-- {
				slashes++
			}
			if slashes%2 == 0 {
				end = i
				break
			}
		}
		repaired := arguments[:end] + `"}`
		var object map[string]json.RawMessage
		if json.Unmarshal([]byte(repaired), &object) != nil || object == nil {
			return "", errors.New("completed string is not valid JSON")
		}
		return repaired, nil
	}
}
