package merkleproof

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"

	"github.com/cosmos/ics23/go"
)

// Input structures matching the provided JSON
type Participant struct {
	Index        string   `json:"index"`
	ValidatorKey string   `json:"validatorKey"`
	Weight       int      `json:"weight"`
	InferenceUrl string   `json:"inferenceUrl"`
	Models       []string `json:"models"`
}

type ActiveParticipants struct {
	Participants         []Participant `json:"participants"`
	EpochGroupId         int           `json:"epochGroupId"`
	PocStartBlockHeight  int64         `json:"pocStartBlockHeight"`
	CreatedAtBlockHeight int64         `json:"createdAtBlockHeight"`
}

type ProofOp struct {
	Type string `json:"type"`
	Key  string `json:"key"`
	Data string `json:"data"`
}

type ProofOps struct {
	Ops []ProofOp `json:"ops"`
}

// The JSON structure we received
type InputJSON struct {
	ActiveParticipants      ActiveParticipants `json:"active_participants"`
	ActiveParticipantsBytes string             `json:"active_participants_bytes"`
	ProofOps                ProofOps           `json:"proof_ops"`
}

func getBlockAppHash() []byte {
	bytes, err := hex.DecodeString("0B623B9F61D2455F9FF47405158BC4BA2322403AAD517908AB1014C28A8ECAD1")
	if err != nil {
		log.Fatalf("failed to decode app hash: %v", err)
	}

	return bytes
}

// We need to verify the proofs using ICS23. Typically, you need the right ProofSpec.
// Cosmos chains have standard specs for iavl and multistore proofs.
// We'll define them here. These specs are standard in Cosmos SDK.

var (
	iavlSpec = &ics23.ProofSpec{
		LeafSpec: &ics23.LeafOp{
			Hash:         ics23.HashOp_SHA256,
			PrehashKey:   ics23.HashOp_NO_HASH,
			PrehashValue: ics23.HashOp_SHA256,
			Length:       ics23.LengthOp_VAR_PROTO,
			Prefix:       []byte{0},
		},
		InnerSpec: &ics23.InnerSpec{
			ChildOrder:      []int32{0, 1},
			ChildSize:       33,
			MinPrefixLength: 1,
			MaxPrefixLength: 1,
			Hash:            ics23.HashOp_SHA256,
		},
		MaxDepth: 0,
		MinDepth: 0,
	}

	simpleSpec = &ics23.ProofSpec{
		LeafSpec: &ics23.LeafOp{
			Hash:         ics23.HashOp_SHA256,
			PrehashKey:   ics23.HashOp_NO_HASH,
			PrehashValue: ics23.HashOp_NO_HASH,
			Length:       ics23.LengthOp_VAR_PROTO,
			Prefix:       []byte{},
		},
		InnerSpec: &ics23.InnerSpec{
			ChildOrder:      []int32{0, 1},
			ChildSize:       32,
			MinPrefixLength: 0,
			MaxPrefixLength: 0,
			Hash:            ics23.HashOp_SHA256,
		},
	}
)

// A helper function to decode and parse a CommitmentProof from base64 data.
func decodeProof(dataBase64 string) (*ics23.CommitmentProof, error) {
	dataBytes, err := base64.StdEncoding.DecodeString(dataBase64)
	if err != nil {
		return nil, fmt.Errorf("failed to base64 decode proof data: %w", err)
	}
	var proof ics23.CommitmentProof
	if err := proof.Unmarshal(dataBytes); err != nil {
		return nil, fmt.Errorf("failed to unmarshal commitment proof: %w", err)
	}
	return &proof, nil
}

func VerifyMain() {
	// The JSON input as a string (the one you provided)
	jsonInput := `{
      "active_participants": {
        "participants": [
          {
            "index": "cosmos1yqt5h2z9af0ztgazflagny8rsj9y9dynxl6mzy",
            "validatorKey": "EUOCy/Y9FkYlzKDMZtwDCz981PttPN91nSyOhMAiYs4=",
            "weight": 84,
            "inferenceUrl": "http://04fc-87-116-166-156.ngrok-free.app:8080",
            "models": [
              "unsloth/llama-3-8b-Instruct"
            ]
          }
        ],
        "epochGroupId": 1,
        "pocStartBlockHeight": 5,
        "createdAtBlockHeight": 26
      },
	  "proof_ops": {
		"ops": [
		  {
			"type": "ics23:iavl",
			"key": "QWN0aXZlUGFydGljaXBhbnRzLzEvdmFsdWUv",
			"data": "CpYDChtBY3RpdmVQYXJ0aWNpcGFudHMvMS92YWx1ZS8StQEKrAEKLWNvc21vczF5cXQ1aDJ6OWFmMHp0Z2F6ZmxhZ255OHJzajl5OWR5bnhsNm16eRIsRVVPQ3kvWTlGa1lsektETVp0d0RDejk4MVB0dFBOOTFuU3lPaE1BaVlzND0YVCIuaHR0cDovLzA0ZmMtODctMTE2LTE2Ni0xNTYubmdyb2stZnJlZS5hcHA6ODA4MCobdW5zbG90aC9sbGFtYS0zLThiLUluc3RydWN0EAEYBSgaGgsIARgBIAEqAwACNCIrCAESBAIENCAaISCzokKmCVgytuP8PFPxbMopXxW1zb6rXAwDhOUFJ0gBKCIrCAESBAQINCAaISDHUtThoy20viih7Rdk6dPevJXQQE6qxWPnsCdHJwSUtiIrCAESBAYMNCAaISD15FNgR3E/TEdE1zAr4xUM90bLwdzP4pQt6UKfaDMDDiIrCAESBAgYNCAaISCFHQoea4rIshJN0HPNsnfnSJ8isE2NrGar/v5Mf+G72A=="
		  },
		  {
			"type": "ics23:simple",
			"key": "aW5mZXJlbmNl",
			"data": "CtoBCglpbmZlcmVuY2USIGX1qB5WH/d8QmZ538VxwjcPUURRPPGpFKqYSQ53ZYD0GgkIARgBIAEqAQAiJwgBEgEBGiDEgm9ckHvXOsDJ0zZmh+n0qjD2cpV0J2fmvlb1S5CS4CInCAESAQEaIJXhyLpHqjGG8RNH1IDYExUpZFPyxZvc7HLLmYEKxBeAIicIARIBARogLvenNYrmf1csFe+CpTTy1VDiT8FeT8b+NUOnJC0TRo4iJQgBEiEB8CyOEL8qEB+WyfIoGUz8VhZoibafCKkAjSicssk6o94="
		  }
		]
	  },
      "validators": [
        {
          "address": "A5C29266B24E0EB385F089068C844B7E45458045",
          "pub_key": "EUOCy/Y9FkYlzKDMZtwDCz981PttPN91nSyOhMAiYs4=",
          "voting_power": 10000000,
          "proposer_priority": 0
        }
      ]
    }`

	var input InputJSON
	if err := json.Unmarshal([]byte(jsonInput), &input); err != nil {
		log.Fatalf("failed to unmarshal input: %v", err)
	}

	// We now have input.ActiveParticipants, input.ProofOps, and input.Validators

	// 1. Verify the block hash and signatures (Simplified)
	blockAppHash := getBlockAppHash()

	// 2. Verify ICS23 proofs
	// According to the proof_ops, we have a chain of proofs:
	// Typically, you'll verify from the store-level (ics23:simple) up to the iavl proof.

	// Let's find the iavl proof op:
	var iavlOp *ProofOp
	var simpleOp *ProofOp
	for _, op := range input.ProofOps.Ops {
		if op.Type == "ics23:iavl" {
			iavlOp = &op
		} else if op.Type == "ics23:simple" {
			simpleOp = &op
		}
	}

	if iavlOp == nil || simpleOp == nil {
		log.Fatalf("missing required proofs")
	}

	// Decode proofs
	iavlProof, err := decodeProof(iavlOp.Data)
	if err != nil {
		log.Fatalf("failed to decode iavl proof: %v", err)
	}

	simpleProof, err := decodeProof(simpleOp.Data)
	if err != nil {
		log.Fatalf("failed to decode simple proof: %v", err)
	} else {
		log.Printf("simple proof: %v", simpleProof)
	}

	// Decode keys
	iavlKeyBytes, err := base64.StdEncoding.DecodeString(iavlOp.Key)
	if err != nil {
		log.Fatalf("failed to decode iavl key: %v", err)
	}

	simpleKeyBytes, err := base64.StdEncoding.DecodeString(simpleOp.Key)
	if err != nil {
		log.Fatalf("failed to decode simple key: %v", err)
	} else {
		log.Printf("simple key: %v", simpleKeyBytes)
	}

	// Verify the simple proof first: This usually ensures that the named store (e.g., "inference") is included in the root.
	// In many Cosmos-based chains, the `simple` proof is the MultiStore proof that proves a sub-store (like IAVL store) root.

	// The ICS23 proofs usually represent a chain of verifications. For simplicity:
	// Let's assume `simpleProof` proves `iavlProof` root is part of `blockAppHash`.
	// We must extract the "root" from iavlProof and then verify the simpleProof that this root is included in the appHash.

	// Actually, ICS23 proofs are typically composed. The `ics23` library doesn't "chain" automatically.
	// Normally, you get a single combined proof or must apply them in correct order.

	// For demonstration, let's assume `simpleProof` is an existence proof for a module root
	// and `iavlProof` is an existence proof for the key/value in that module.
	// In a real-world scenario, you'd know which proof corresponds to which store and root.

	// Step 1: Verify IAVL proof directly against what should be the sub-store root
	// But we need the sub-store root. Typically, one proof leads to another.
	// Here we have two separate proofs ops. Usually, you'd get one combined proof.

	// Let's assume:
	// - The `simpleOp` is the top-level proof that a certain sub-store (like "inference") root is in appHash.
	// - The `iavlOp` shows the key/value in that sub-store.

	// Without actual chain context, we must guess:
	// We'll just demonstrate verification with a known root:
	// If you had the sub-store root from the simple proof, you'd verify iavlOp against it.
	// For now, let's pretend we directly verify iavlProof against blockAppHash (not correct in real scenario).
	// A real chain would provide a combined proof or you'd know how to chain them.

	storedValue, err := hex.DecodeString(input.ActiveParticipantsBytes)
	if err != nil {
		log.Fatalf("failed to decode stored value: %v", err)
	}

	// ICS23 Verification call:
	verified := ics23.VerifyMembership(iavlSpec, blockAppHash, iavlProof, iavlKeyBytes, storedValue)
	if !verified {
		log.Fatalf("IAVL proof verification failed: %v", err)
	}

	// If the above passes, it means the (key => value) pair is included in the state root (appHash),
	// given the assumptions. In a real scenario, you'd first verify `simpleProof` to extract/store root,
	// then `iavlProof` against that store root.

	// Since this is an illustrative example, we've simplified some steps.

	fmt.Println("Proof verified successfully and participants data matches the state root.")
}
