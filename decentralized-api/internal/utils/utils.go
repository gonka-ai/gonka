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
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/cometbft/cometbft/crypto/tmhash"
	rpcclient "github.com/cometbft/cometbft/rpc/client/http"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/gonka-ai/gonka-utils/go/contracts"
	externalutils "github.com/gonka-ai/gonka-utils/go/utils"
	"github.com/productscience/inference/x/inference/types"
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

func QueryActiveParticipants(rpcClient *rpcclient.HTTP, queryClient types.QueryClient) externalutils.GetParticipantsFn {
	return func(ctx context.Context, epochParam string) (*contracts.ActiveParticipantWithProof, error) {
		if epochParam == "" {
			return nil, errors.New("invalid epoch id")
		}

		var epochId uint64
		if epochParam == "current" {
			currEpoch, err := queryClient.GetCurrentEpoch(ctx, &types.QueryGetCurrentEpochRequest{})
			if err != nil {
				logging.Error("Failed to get current epoch", types.Participants, "error", err)
				return nil, err
			}
			logging.Info("Current epoch resolved.", types.Participants, "epoch", currEpoch.Epoch)
			epochId = currEpoch.Epoch
		} else {
			epoch, err := strconv.ParseUint(epochParam, 10, 64)
			if err != nil {
				return nil, errors.New("invalid epoch id")
			}
			epochId = epoch
		}

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
		queryWIthProof := true
		if blockHeight <= 1 {
			// cosmos can't build merkle proof for genesis because need previous state
			queryWIthProof = false
		}

		result, err = cosmos_client.QueryByKeyWithOptions(rpcClient, "inference", dataKey, blockHeight, queryWIthProof)
		if err != nil {
			logging.Error("Failed to query active participant. Req 2", types.Participants, "error", err)
			return nil, err
		}

		blockProof, err := queryClient.GetBlockProofByHeight(ctx, &types.QueryBlockProofRequest{ProofHeight: activeParticipants.CreatedAtBlockHeight})
		if err != nil {
			logging.Error("Failed to get block proof by height", types.Participants, "error", err)
			return nil, err
		}

		if result.Response.ProofOps != nil {
			hash, _ := hex.DecodeString(blockProof.Proof.AppHashHex)
			verifyProof(epochId, result, hash)
		}

		validatorsProof, err := queryClient.GetValidatorsProofByHeight(ctx, &types.QueryGetValidatorsProofRequest{ProofHeight: activeParticipants.CreatedAtBlockHeight})
		if err != nil {
			return nil, err
		}

		activeParticipantsBytes := hex.EncodeToString(result.Response.Value)
		addresses := make([]string, len(activeParticipants.Participants))
		for i, participant := range activeParticipants.Participants {
			addresses[i], err = pubKeyToAddress3(participant.ValidatorKey)
			if err != nil {
				logging.Error("Failed to convert public key to address", types.Participants, "error", err)
			}
		}

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
			var seed *contracts.RandomSeed
			if participant.Seed != nil {
				seed = &contracts.RandomSeed{
					Participant: participant.Seed.Participant,
					EpochIndex:  participant.Seed.EpochIndex,
					Signature:   participant.Seed.Signature,
				}
			}
			finalParticipants.Participants[i] = &contracts.ActiveParticipant{
				Index:        participant.Index,
				ValidatorKey: participant.ValidatorKey,
				Weight:       participant.Weight,
				InferenceUrl: participant.InferenceUrl,
				Models:       participant.Models,
				Seed:         seed,
			}
			if err != nil {
				logging.Error("Failed to convert public key to address", types.Participants, "error", err)
			}
		}

		block := &contracts.BlockProof{
			CreatedAtBlockHeight: blockProof.Proof.CreatedAtBlockHeight,
			AppHashHex:           blockProof.Proof.AppHashHex,
			TotalVotingPower:     blockProof.Proof.TotalVotingPower,
			Commits:              make([]*contracts.CommitInfo, 0),
		}

		for _, commit := range blockProof.Proof.Commits {
			block.Commits = append(block.Commits, &contracts.CommitInfo{
				ValidatorAddress: commit.ValidatorAddress,
				ValidatorPubKey:  commit.ValidatorPubKey,
				VotingPower:      commit.VotingPower,
			})
		}

		validators := &contracts.ValidatorsProof{
			BlockHeight: validatorsProof.Proof.BlockHeight,
			Round:       validatorsProof.Proof.Round,
			BlockId: &contracts.BlockID{
				Hash:               validatorsProof.Proof.BlockId.Hash,
				PartSetHeaderTotal: validatorsProof.Proof.BlockId.PartSetHeaderTotal,
				PartSetHeaderHash:  validatorsProof.Proof.BlockId.PartSetHeaderHash,
			},
			Signatures: make([]*contracts.SignatureInfo, 0),
		}

		for _, sign := range validatorsProof.Proof.Signatures {
			validators.Signatures = append(validators.Signatures, &contracts.SignatureInfo{
				SignatureBase64:     sign.SignatureBase64,
				ValidatorAddressHex: sign.ValidatorAddressHex,
				Timestamp:           sign.Timestamp,
			})
		}

		return &contracts.ActiveParticipantWithProof{
			ActiveParticipants:      finalParticipants,
			Addresses:               addresses,
			ActiveParticipantsBytes: activeParticipantsBytes,
			ProofOps:                result.Response.ProofOps,
			BlockProof:              block,
			ValidatorsProof:         validators,
		}, nil
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

func verifyProof(epoch uint64, result *coretypes.ResultABCIQuery, appHash []byte) {
	dataKey := types.ActiveParticipantsFullKey(epoch)
	// Build the key path used by proof verification. We percent-encode the raw
	// binary key so the path is a valid UTF-8/URL string.
	verKey := "/inference/" + url.PathEscape(string(dataKey))
	// verKey2 := string(result.Response.Key)
	logging.Info("Attempting verification", types.Participants, "verKey", verKey)
	err := merkleproof.VerifyUsingProofRt(result.Response.ProofOps, appHash, verKey, result.Response.Value)
	if err != nil {
		logging.Error("VerifyUsingProofRt failed", types.Participants, "error", err)
	}

	err = merkleproof.VerifyUsingMerkleProof(result.Response.ProofOps, appHash, "inference", string(dataKey), result.Response.Value)
	if err != nil {
		logging.Error("VerifyUsingMerkleProof failed", types.Participants, "error", err)
	}
}
