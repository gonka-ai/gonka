package keeper

import (
	"context"
	sdkerrors "cosmossdk.io/errors"
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

func (k msgServer) SubmitPoC(goCtx context.Context, msg *types.MsgSubmitPoC) (*types.MsgSubmitPoCResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	currentBlockHeight := ctx.BlockHeight()
	startBlockHeight := msg.BlockHeight

	if !proofofcompute.IsStartOfPoCStage(startBlockHeight) {
		errMsg := fmt.Sprintf("start block height must be divisible by %d. msg.BlockHeight = %d", proofofcompute.EpochLength, startBlockHeight)
		return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight, errMsg)
	}

	if !proofofcompute.IsPoCExchangeWindow(startBlockHeight, currentBlockHeight) {
		errMsg := fmt.Sprintf("msg.BlockHeight = %d, currentBlockHeight = %d", startBlockHeight, currentBlockHeight)
		return nil, sdkerrors.Wrap(types.ErrPocTooLate, errMsg)
	}

	// 1. Get block hash from startBlockHeight
	blockHash, err := k.getBlockHash(startBlockHeight)
	if err != nil {
		return nil, err
	}

	// 2. Get signer public key
	pubKey, err := k.getMsgSignerPubKey(msg, ctx)
	if err != nil {
		return nil, err
	}

	// 3. Verify all nonces
	input := proofofcompute.GetInput(blockHash, string(pubKey.Bytes()))
	for _, n := range msg.Nonce {
		proof := proofofcompute.ProofOfCompute(input, []byte(n))
		if !proofofcompute.AcceptHash(proof.Hash, proofofcompute.DefaultDifficulty) {
			return nil, sdkerrors.Wrap(types.ErrPocNonceNotAccepted, "invalid nonce")
		}
	}

	// 4. Store power
	power := len(msg.Nonce)
	k.Keeper.SetPower(ctx, types.Power{
		ParticipantAddress:       msg.Creator,
		Power:                    int64(power),
		PocStageStartBlockHeight: startBlockHeight,
		ReceivedAtBlockHeight:    currentBlockHeight,
	})

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
	account := k.AccountKeeper.GetAccount(ctx, addr)
	pubKey := account.GetPubKey()
	return pubKey, nil
}
