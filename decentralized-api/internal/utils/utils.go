package utils

import (
	"context"
	"decentralized-api/completionapi"
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"decentralized-api/merkleproof"
	"decentralized-api/utils"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/cometbft/cometbft/crypto/tmhash"
	rpcclient "github.com/cometbft/cometbft/rpc/client/http"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	comettypes "github.com/cometbft/cometbft/types"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/gonka-ai/gonka-utils/go/contracts"
	externalutils "github.com/gonka-ai/gonka-utils/go/utils"
	"github.com/productscience/inference/x/inference/types"
	"net/url"
	"strings"
)

// UnquoteEventValue removes JSON quotes from event values
// Cosmos SDK events often have JSON-encoded values like "\"1\"" which need to be unquoted to "1"
func UnquoteEventValue(value string) (string, error) {
	var unquoted string
	err := json.Unmarshal([]byte(value), &unquoted)
	if err != nil {
		return value, nil // Return original value if unquoting fails
	}
	return unquoted, nil
}

// DecodeBase64IfPossible attempts to decode a string as base64
// Returns the decoded bytes if successful, or an error if not valid base64
func DecodeBase64IfPossible(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

// DecodeHex decodes a hex string to bytes
// Returns the decoded bytes if successful, or an error if not valid hex
func DecodeHex(s string) ([]byte, error) {
	return hex.DecodeString(s)
}

func GetResponseHash(bodyBytes []byte) (string, *completionapi.Response, error) {
	var response completionapi.Response
	if err := json.Unmarshal(bodyBytes, &response); err != nil {
		return "", nil, err
	}

	var content string
	for _, choice := range response.Choices {
		content += choice.Message.Content
	}
	hash := utils.GenerateSHA256Hash(content)
	return hash, &response, nil
}

func QueryActiveParticipants(rpcClient *rpcclient.HTTP, epochId uint64) externalutils.GetParticipantsFn {
	return func(ctx context.Context, _ string) (*contracts.ActiveParticipantWithProof, error) {
		dataKey := types.ActiveParticipantsFullKey(epochId)
		result, err := cosmos_client.QueryByKey(rpcClient, "inference", dataKey)
		if err != nil {
			logging.Error("Failed to query active participants. Req 1", types.Participants, "error", err)
			return nil, err
		}

		logging.Info("[PARTICIPANTS-DEBUG] Raw active participants query result", types.Participants,
			"epoch", epochId,
			"value_bytes", len(result.Response.Value))

		interfaceRegistry := codectypes.NewInterfaceRegistry()
		types.RegisterInterfaces(interfaceRegistry)

		cdc := codec.NewProtoCodec(interfaceRegistry)

		var activeParticipants types.ActiveParticipants
		if err := cdc.Unmarshal(result.Response.Value, &activeParticipants); err != nil {
			logging.Error("Failed to unmarshal active participants. Req 1", types.Participants, "error", err)
			return nil, err
		}

		logging.Info("[PARTICIPANTS-DEBUG] Unmarshalled ActiveParticipants", types.Participants,
			"epoch", epochId,
			"created_at_block_height", activeParticipants.CreatedAtBlockHeight,
			"effective_block_height", activeParticipants.EffectiveBlockHeight)

		blockHeight := activeParticipants.CreatedAtBlockHeight
		result, err = cosmos_client.QueryByKeyWithOptions(rpcClient, "inference", dataKey, blockHeight, true)
		if err != nil {
			logging.Error("Failed to query active participant. Req 2", types.Participants, "error", err)
			return nil, err
		}

		block, err := rpcClient.Block(context.Background(), &activeParticipants.CreatedAtBlockHeight)
		if err != nil || block == nil {
			logging.Error("Failed to get block", types.Participants, "error", err)
			return nil, err
		}

		heightP1 := activeParticipants.CreatedAtBlockHeight + 1
		blockP1, err := rpcClient.Block(context.Background(), &heightP1)
		if err != nil || blockP1 == nil {
			logging.Error("Failed to get block + 1", types.Participants, "error", err)
		}

		if result.Response.ProofOps != nil {
			verifyProof(epochId, result, blockP1)
		}

		vals, err := QueryValidators(rpcClient)(context.Background(), activeParticipants.CreatedAtBlockHeight)
		if err != nil || vals == nil {
			logging.Error("Failed to get validators", types.Participants, "error", err)
			return nil, err
		}

		activeParticipantsBytes := hex.EncodeToString(result.Response.Value)
		addresses := make([]string, len(activeParticipants.Participants))

		finalParticipants := contracts.ActiveParticipants{
			Participants:         make([]*contracts.ActiveParticipant, len(activeParticipants.Participants)),
			EpochGroupId:         activeParticipants.EpochGroupId,
			PocStartBlockHeight:  activeParticipants.PocStartBlockHeight,
			EffectiveBlockHeight: activeParticipants.EffectiveBlockHeight,
			CreatedAtBlockHeight: activeParticipants.CreatedAtBlockHeight,
			EpochId:              activeParticipants.EpochId,
		}

		for i, participant := range activeParticipants.Participants {
			addresses[i], err = pubKeyToAddress3(participant.ValidatorKey)
			finalParticipants.Participants[i] = &contracts.ActiveParticipant{
				Index:        participant.Index,
				ValidatorKey: participant.ValidatorKey,
				Weight:       participant.Weight,
				InferenceUrl: participant.InferenceUrl,
				Models:       participant.Models,
				Seed: &contracts.RandomSeed{
					Participant: participant.Seed.Participant,
					BlockHeight: participant.Seed.BlockHeight,
					Signature:   participant.Seed.Signature,
				},
			}
			if err != nil {
				logging.Error("Failed to convert public key to address", types.Participants, "error", err)
			}
		}

		var returnBlock *comettypes.Block
		if blockP1 != nil {
			returnBlock = blockP1.Block
		}

		return &contracts.ActiveParticipantWithProof{
			ActiveParticipants:      finalParticipants,
			Addresses:               addresses,
			ActiveParticipantsBytes: activeParticipantsBytes,
			ProofOps:                result.Response.ProofOps,
			Validators:              vals.Validators,
			Block:                   returnBlock,
		}, err
	}
}

func QueryValidators(rpcClient *rpcclient.HTTP) externalutils.GetValidatorsFn {
	return func(ctx context.Context, height int64) (*contracts.BlockValidators, error) {
		vals, err := rpcClient.Validators(context.Background(), &height, nil, nil)
		if err != nil || vals == nil {
			logging.Error("Failed to get validators", types.Participants, "error", err)
			return nil, err
		}

		validators := make([]*contracts.Validator, len(vals.Validators))
		for i, validator := range vals.Validators {
			validators[i] = &contracts.Validator{
				Address:          validator.PubKey.Address().String(),
				PubKey:           base64.StdEncoding.EncodeToString(validator.PubKey.Bytes()),
				VotingPower:      validator.VotingPower,
				ProposerPriority: validator.ProposerPriority,
			}
		}

		return &contracts.BlockValidators{
			BlockHeight: vals.BlockHeight,
			Validators:  validators,
			Count:       vals.Count,
			Total:       vals.Total,
		}, nil
	}
}

func QueryBlock(rpcClient *rpcclient.HTTP) externalutils.GetBlockFn {
	return func(ctx context.Context, height int64) (*coretypes.ResultBlock, error) {
		return rpcClient.Block(context.Background(), &height)
	}
}

func pubKeyToAddress3(pubKey string) (string, error) {
	pubKeyBytes, err := base64.StdEncoding.DecodeString(pubKey)
	if err != nil {
		return "", err
	}

	valAddr := tmhash.SumTruncated(pubKeyBytes)
	valAddrHex := strings.ToUpper(hex.EncodeToString(valAddr))
	return valAddrHex, nil
}

func verifyProof(epoch uint64, result *coretypes.ResultABCIQuery, block *coretypes.ResultBlock) {
	dataKey := types.ActiveParticipantsFullKey(epoch)
	// Build the key path used by proof verification. We percent-encode the raw
	// binary key so the path is a valid UTF-8/URL string.
	verKey := "/inference/" + url.PathEscape(string(dataKey))
	// verKey2 := string(result.Response.Key)
	logging.Info("Attempting verification", types.Participants, "verKey", verKey)
	err := merkleproof.VerifyUsingProofRt(result.Response.ProofOps, block.Block.AppHash, verKey, result.Response.Value)
	if err != nil {
		logging.Error("VerifyUsingProofRt failed", types.Participants, "error", err)
	}

	err = merkleproof.VerifyUsingMerkleProof(result.Response.ProofOps, block.Block.AppHash, "inference", string(dataKey), result.Response.Value)
	if err != nil {
		logging.Error("VerifyUsingMerkleProof failed", types.Participants, "error", err)
	}
}
