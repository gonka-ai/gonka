package merkleproof

import (
	"context"
	"cosmossdk.io/store/rootmulti"
	"fmt"
	cryptotypes "github.com/cometbft/cometbft/proto/tendermint/crypto"
	"github.com/cometbft/cometbft/rpc/client/http"
	comettypes "github.com/cometbft/cometbft/types"
	ibctypes "github.com/cosmos/ibc-go/v8/modules/core/23-commitment/types"
)

func VerifyBlockSignatures(address string, height int64) error {
	// Step 1: Create a new RPC client
	rpcClient, err := http.New(address, "/websocket")
	if err != nil {
		return err
	}

	// Step 2: Get the block and its commit at the desired height
	blockRes, err := rpcClient.Block(context.Background(), &height)
	if err != nil {
		return err
	}
	block := blockRes.Block
	commit := blockRes.Block.LastCommit

	// Step 3: Get the validator set at height - 1 (previous height)
	valSetRes, err := rpcClient.Validators(context.Background(), &height, nil, nil)
	if err != nil {
		return err
	}
	valSet := valSetRes.Validators

	// Step 4: Verify the signatures
	err = VerifyCommit(block.Header.ChainID, commit, &block.Header, valSet)
	if err != nil {
		return fmt.Errorf("block signature verification failed: %v", err)
	}

	fmt.Println("Block signature verification successful!")
	return nil
}

func VerifyCommit(chainID string, commit *comettypes.Commit, header *comettypes.Header, validators []*comettypes.Validator) error {
	// Reconstruct the validator set
	valSet := comettypes.NewValidatorSet(validators)

	// Verify the commit signatures against the validator set
	if err := valSet.VerifyCommit(chainID, commit.BlockID, header.Height-1, commit); err != nil {
		return fmt.Errorf("invalid commit signatures")
	}

	return nil
}

/*
	func VerifyProof(proofOps *cryptotypes.ProofOps, key, value, appHash []byte) error {
		merkleProof, err := ibctypes.ConvertProofs(proofOps)
		merkleProof.Verify()

		// Important to use runtime from the rootmulti package
		proofRt := rootmulti.DefaultProofRuntime()
		proofRt.VerifyValue(proofOps, key, value, appHash)

		proofOperator, err := merkle.ValueOpDecoder(proofOps.Ops[0])
		if err != nil {
			return err
		}

		// Compute the root hash from the proof
		rootHash := proofOperator.()
		// OR THIS: merkleProof.Verify(rootHash, value)

		// Compare the computed root hash with the app hash
		if !bytes.Equal(rootHash, appHash) {
			return fmt.Errorf("computed root hash does not match app hash")
		}

		return nil
	}
*/
func VerifyUsingProofRt(proofOps *cryptotypes.ProofOps, root []byte, keypath string, value []byte) error {
	proofRt := rootmulti.DefaultProofRuntime()
	return proofRt.VerifyValue(proofOps, root, keypath, value)
}

func VerifyUsingMerkleProof(proofOps *cryptotypes.ProofOps, root []byte, moduleKey string, valueKey string, value []byte) error {
	merkleProof, err := ibctypes.ConvertProofs(proofOps)
	if err != nil {
		return err
	}

	merkleRoot := ibctypes.MerkleRoot{Hash: root}
	path := ibctypes.MerklePath{KeyPath: []string{moduleKey, valueKey}}

	return merkleProof.VerifyMembership(ibctypes.GetSDKSpecs(), merkleRoot, path, value)
}
