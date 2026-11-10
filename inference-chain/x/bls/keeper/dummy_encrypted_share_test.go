package keeper_test

import "github.com/productscience/inference/x/bls/types"

func dummyEncryptedShare(seed byte) []byte {
	share := make([]byte, types.MinEncryptedShareCiphertextLen)
	share[0] = seed
	return share
}

func dummyEncryptedShares(n int) [][]byte {
	shares := make([][]byte, n)
	for i := range shares {
		shares[i] = dummyEncryptedShare(byte(i + 1))
	}
	return shares
}
