package completionapi

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode"
)

const UnknownVocabularyTokenIDLimit = 9_999_999

func TokenIDLimit(vocabularySize int) int64 {
	if vocabularySize > 0 {
		return int64(vocabularySize)
	}
	return UnknownVocabularyTokenIDLimit
}

func boundTokenIDs(requestMap map[string]interface{}, tokenIDLimit int64) {
	delete(requestMap, "enforced_tokens")

	if bias, isObject := requestMap["logit_bias"].(map[string]interface{}); isObject {
		for key := range bias {
			if TokenIDKeyOutOfRange(key, tokenIDLimit) {
				delete(bias, key)
			}
		}
		if len(bias) == 0 {
			delete(requestMap, "logit_bias")
		}
	}

	if ids, isArray := requestMap["allowed_token_ids"].([]interface{}); isArray {
		kept := make([]interface{}, 0, len(ids))
		for _, id := range ids {
			if !tokenIDValueOutOfRange(id, tokenIDLimit) {
				kept = append(kept, id)
			}
		}
		requestMap["allowed_token_ids"] = kept
	}
}

func TokenIDKeyOutOfRange(key string, tokenIDLimit int64) bool {
	key, ok := normalizeTokenIDKey(key)
	if !ok {
		return false
	}
	tokenID, err := strconv.ParseInt(key, 10, 64)
	if err != nil {
		return errors.Is(err, strconv.ErrRange)
	}
	return tokenID < 0 || tokenID >= tokenIDLimit
}

func tokenIDValueOutOfRange(value interface{}, tokenIDLimit int64) bool {
	number, isNumber := value.(float64)
	if !isNumber || number != math.Trunc(number) {
		return false
	}
	return number < 0 || number >= float64(tokenIDLimit)
}

func normalizeTokenIDKey(key string) (string, bool) {
	key = strings.TrimSpace(key)
	var normalized strings.Builder
	normalized.Grow(len(key))
	if len(key) > 0 && (key[0] == '+' || key[0] == '-') {
		normalized.WriteByte(key[0])
		key = key[1:]
	}
	previousDigit := false
	for _, char := range key {
		if char == '_' && previousDigit {
			previousDigit = false
			continue
		}
		digit, ok := decimalTokenIDDigit(char)
		if !ok {
			return "", false
		}
		normalized.WriteByte('0' + digit)
		previousDigit = true
	}
	return normalized.String(), previousDigit
}

func decimalTokenIDDigit(char rune) (byte, bool) {
	if char >= '0' && char <= '9' {
		return byte(char - '0'), true
	}
	for _, interval := range unicode.Digit.R16 {
		if uint32(char) >= uint32(interval.Lo) && uint32(char) <= uint32(interval.Hi) && (uint32(char)-uint32(interval.Lo))%uint32(interval.Stride) == 0 {
			return byte((uint32(char) - uint32(interval.Lo)) / uint32(interval.Stride) % 10), true
		}
	}
	for _, interval := range unicode.Digit.R32 {
		if uint32(char) >= interval.Lo && uint32(char) <= interval.Hi && (uint32(char)-interval.Lo)%interval.Stride == 0 {
			return byte((uint32(char) - interval.Lo) / interval.Stride % 10), true
		}
	}
	return 0, false
}
