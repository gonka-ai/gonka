package merkleproof

import (
	"context"
	"fmt"
	"github.com/cometbft/cometbft/rpc/core/types"
	comettypes "github.com/cometbft/cometbft/types"
	"log"

	rpcclient "github.com/cometbft/cometbft/rpc/client"
	"github.com/cometbft/cometbft/rpc/client/http"
)

func QueryWithProof(rpcClient *http.HTTP, storeKey, dataKey string) (*coretypes.ResultABCIQuery, error) {
	log.Printf("Querying store %s with key %s...\n", storeKey, dataKey)

	key := []byte(dataKey)
	path := fmt.Sprintf("store/%s/key", storeKey)

	response, err := rpcClient.ABCIQueryWithOptions(context.Background(), path, key, rpcclient.ABCIQueryOptions{Prove: true})

	return response, err
}

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

/*func VerifyProof(proofOps *cryptotypes.ProofOps, key, value, appHash []byte) error {
	// Convert ProofOps to Merkle proof
	merkleProof, err := merkle.ProofFromProto(proofOps.Ops[0].GetData())
	if err != nil {
		return err
	}

	// Compute the root hash from the proof
	rootHash := merkleProof.ComputeRootHash(key, value)

	// Compare the computed root hash with the app hash
	if !bytes.Equal(rootHash, appHash) {
		return fmt.Errorf("computed root hash does not match app hash")
	}

	return nil
}*/
