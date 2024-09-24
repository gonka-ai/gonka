package merkleproof

import (
	"context"
	"fmt"
	"github.com/cometbft/cometbft/rpc/core/types"
	"log"

	rpcclient "github.com/cometbft/cometbft/rpc/client"
	"github.com/cometbft/cometbft/rpc/client/http"
)

func QueryWithProof(storeKey, dataKey string) (*coretypes.ResultABCIQuery, error) {
	log.Printf("Querying store %s with key %s...\n", storeKey, dataKey)
	// Create a new RPC client
	rpcClient, err := http.New("http://localhost:26657", "/websocket")
	if err != nil {
		panic(err)
	}

	key := []byte(dataKey)
	path := fmt.Sprintf("store/%s/key", storeKey)

	return rpcClient.ABCIQueryWithOptions(context.Background(), path, key, rpcclient.ABCIQueryOptions{Prove: true})
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
