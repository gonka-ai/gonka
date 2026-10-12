package keeper

import "github.com/productscience/inference/x/bls/types"

var VerifyDealerConstantTermPoK = verifyDealerConstantTermPoK

func admittedPoK() []byte {
	return make([]byte, types.DealerConstantTermPoKLen)
}
