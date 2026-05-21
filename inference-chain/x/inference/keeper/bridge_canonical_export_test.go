package keeper

import "github.com/productscience/inference/x/inference/types"

// Test-only exports for bridge canonicalization regression tests.
func BuildCanonicalBridgeTransactionForTest(msg *types.MsgBridgeExchange) (*types.BridgeTransaction, error) {
	return buildCanonicalBridgeTransaction(msg)
}

func GenerateSecureBridgeTransactionKeyForTest(tx *types.BridgeTransaction) string {
	return generateSecureBridgeTransactionKey(tx)
}

func CanonicalBridgeDepositEventIDForTest(tx *types.BridgeTransaction) string {
	return canonicalBridgeDepositEventID(tx)
}
