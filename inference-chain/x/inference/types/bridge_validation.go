package types

import (
	"encoding/hex"
	"regexp"
	"strings"
)

var bridgeDigits = regexp.MustCompile(`^[0-9]+$`) //nolint:forbidigo // init code

// decimalStringWithoutLeadingZeros reports whether s is a non-negative base-10
// integer string without redundant leading zeros (except "0" itself).
func decimalStringWithoutLeadingZeros(s string) bool {
	if s == "" || !bridgeDigits.MatchString(s) {
		return false
	}
	return len(s) == 1 || s[0] != '0'
}

// isValidReceiptsRootHex validates Ethereum block receiptsRoot encoding (0x + 32 bytes).
func isValidReceiptsRootHex(root string) bool {
	if len(root) != 66 {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(root), "0x") {
		return false
	}
	_, err := hex.DecodeString(root[2:])
	return err == nil
}

// isValidEthereumAddress validates basic Ethereum address format (0x + 40 hex chars).
func isValidEthereumAddress(address string) bool {
	if len(address) != 42 {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(address), "0x") {
		return false
	}

	hexPart := address[2:]
	for _, r := range hexPart {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}
