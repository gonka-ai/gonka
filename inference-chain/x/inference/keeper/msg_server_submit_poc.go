package keeper

import (
	"context"
	sdkerrors "cosmossdk.io/errors"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	types2 "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/proofofcompute"
	"github.com/productscience/inference/x/inference/types"
	"io"
	"net/http"
)

const PocFailureTag = "PoC [Failure] "

func (k msgServer) SubmitPoC(goCtx context.Context, msg *types.MsgSubmitPoC) (*types.MsgSubmitPoCResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	participant, found := k.GetParticipant(ctx, msg.Creator)
	if !found {
		return nil, errors.New("participant not found")
	}

	if participant.Status == types.ParticipantStatus_INVALID {
		return nil, sdkerrors.Wrap(types.ErrPocAddressInvalid, "participant is invalid")
	}

	currentBlockHeight := ctx.BlockHeight()
	startBlockHeight := msg.BlockHeight
	epochParams := k.Keeper.GetParams(goCtx).EpochParams

	if !epochParams.IsStartOfPoCStage(startBlockHeight) {
		k.LogError(PocFailureTag+"start block height must be divisible by EpochLength", types.PoC, "EpochLength", epochParams.EpochLength, "msg.BlockHeight", startBlockHeight)
		errMsg := fmt.Sprintf("start block height must be divisible by %d. msg.BlockHeight = %d", epochParams.EpochLength, startBlockHeight)
		return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight, errMsg)
	}

	if !epochParams.IsPoCExchangeWindow(startBlockHeight, currentBlockHeight) {
		k.LogError(PocFailureTag+"PoC exchange window is closed.", types.PoC, "msg.BlockHeight", startBlockHeight, "currentBlockHeight", currentBlockHeight)
		errMsg := fmt.Sprintf("msg.BlockHeight = %d, currentBlockHeight = %d", startBlockHeight, currentBlockHeight)
		return nil, sdkerrors.Wrap(types.ErrPocTooLate, errMsg)
	}

	// 1. Get block hash from startBlockHeight
	blockHash, err := k.getBlockHash(startBlockHeight)
	if err != nil {
		k.LogError(PocFailureTag+"Failed to get block hash", types.PoC, "startBlockHeight", startBlockHeight, "err", err)
		return nil, err
	}

	// 2. Get signer public key
	pubKey, err := k.getMsgSignerPubKey(msg, ctx)
	if err != nil {
		k.LogError(PocFailureTag+"Failed to get signer public key", types.PoC, "err", err)
		return nil, err
	}

	k.LogInfo("PoC: Retrieved pub ket", types.PoC, "pubKey", pubKey, "pubKey.String()", pubKey.String())

	// 3. Verify all nonces
	// pubKey.String() yields something like: PubKeySecp256k1{<hex-key>}
	// If you change how you pass it to input here don't forget to change it in decentralized-api as well
	input := proofofcompute.GetInput(blockHash, pubKey.String())
	for _, n := range msg.Nonce {
		nonce, err := hex.DecodeString(n)
		if err != nil {
			k.LogError(PocFailureTag+"Failed to decode nonce.", types.PoC, "nonce", n, "err", err)
			return nil, err
		}
		proof := proofofcompute.ProofOfCompute(input, nonce)

		if !proofofcompute.AcceptHash(proof.Hash, int(k.Keeper.GetParams(goCtx).PocParams.DefaultDifficulty)) {
			k.LogWarn(
				PocFailureTag+"Hash not accepted!", types.PoC, "input", hex.EncodeToString(input), "nonce", n, "hash", proof.Hash,
			)
			return nil, sdkerrors.Wrap(types.ErrPocNonceNotAccepted, "invalid nonce")
		}
	}

	// 4. Store power
	k.LogInfo("Storing power for participant", types.PoC, "participant", msg.Creator, "power", len(msg.Nonce))
	power := len(msg.Nonce)
	k.Keeper.SetUpcomingPower(ctx, types.Power{
		ParticipantAddress:       msg.Creator,
		Power:                    int64(power),
		PocStageStartBlockHeight: startBlockHeight,
		ReceivedAtBlockHeight:    currentBlockHeight,
	})
	group, err := k.Keeper.GetEpochGroup(ctx, uint64(startBlockHeight))
	if err != nil {
		return nil, err
	}
	err = group.AddMember(ctx, msg.Creator, uint64(power), participant.ValidatorKey, msg.SeedSignature)
	if err != nil {
		return nil, err
	}

	return &types.MsgSubmitPoCResponse{}, nil
}

func (k msgServer) getBlockHash(height int64) (string, error) {
	// Send http request to http://localhost:26657/block?height=4000
	url := fmt.Sprintf("http://localhost:26657/block?height=%d", height)
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var responseMap map[string]interface{}
	err = json.Unmarshal(respBytes, &responseMap)
	if err != nil {
		return "", err
	}

	return getBlockHash(responseMap)
}

func getBlockHash(data map[string]interface{}) (string, error) {
	result, ok := data["result"].(map[string]interface{})
	if !ok {
		return "", errors.New("failed to access 'result' key")
	}

	blockID, ok := result["block_id"].(map[string]interface{})
	if !ok {
		return "", errors.New("failed to access 'block_id' key")
	}

	hash, ok := blockID["hash"].(string)
	if !ok {
		return "", errors.New("failed to access 'hash' key or it's not a string")
	}

	return hash, nil
}

func (k msgServer) getMsgSignerPubKey(msg *types.MsgSubmitPoC, ctx sdk.Context) (types2.PubKey, error) {
	addr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return nil, err
	}
	k.LogDebug("Retrieving addr for", types.PoC, "msg.Creator", msg.Creator, "addr", addr)

	account := k.AccountKeeper.GetAccount(ctx, addr)
	pubKey := account.GetPubKey()
	return pubKey, nil
}
