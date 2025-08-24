package inference

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/tx"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/types"
	// this line is used by starport scaffolding # 1
)

var InferenceOperationKeyPerms = []sdk.Msg{
	&types.MsgStartInference{},
	&types.MsgFinishInference{},
	&types.MsgClaimRewards{},
	&types.MsgValidation{},
	&types.MsgSubmitPocBatch{},
	&types.MsgSubmitPocValidation{},
	&types.MsgSubmitSeed{},
	&types.MsgBridgeExchange{},
	&types.MsgSubmitTrainingKvRecord{},
	&types.MsgJoinTraining{},
	&types.MsgJoinTrainingStatus{},
	&types.MsgTrainingHeartbeat{},
	&types.MsgSetBarrier{},
	&types.MsgClaimTrainingTaskForAssignment{},
	&types.MsgAssignTrainingTask{},
	&types.MsgSubmitNewUnfundedParticipant{},
	&types.MsgSubmitHardwareDiff{},
	&types.MsgInvalidateInference{},
	&types.MsgRevalidateInference{},
	&blstypes.MsgSubmitDealerPart{},
	&blstypes.MsgSubmitVerificationVector{},
	&blstypes.MsgRequestThresholdSignature{},
	&blstypes.MsgSubmitPartialSignature{},
	&blstypes.MsgSubmitGroupKeyValidationSignature{},
}

func GrantMLOperationalKeyPermissionsToAccount(
	ctx context.Context,
	clientCtx client.Context,
	txFactory tx.Factory,
	operatorKeyName string,
	aiOperationalAddress sdk.AccAddress,
	expiration *time.Time,
) error {
	operatorInfo, err := clientCtx.Keyring.Key(operatorKeyName)
	if err != nil {
		return fmt.Errorf("failed to get operator key info: %w", err)
	}

	operatorAddress, err := operatorInfo.GetAddress()
	if err != nil {
		return fmt.Errorf("failed to get operator address: %w", err)
	}

	account, err := clientCtx.AccountRetriever.GetAccount(clientCtx, operatorAddress)
	if err != nil {
		return fmt.Errorf("failed to get account details: %w", err)
	}

	txFactory = txFactory.WithAccountNumber(account.GetAccountNumber())
	txFactory = txFactory.WithSequence(account.GetSequence())

	var grantMsgs []sdk.Msg
	var expirationTime time.Time
	if expiration != nil {
		expirationTime = *expiration
	} else {
		expirationTime = time.Now().Add(365 * 24 * time.Hour)
	}

	for _, msgType := range InferenceOperationKeyPerms {
		authorization := authztypes.NewGenericAuthorization(sdk.MsgTypeURL(msgType))
		grantMsg, err := authztypes.NewMsgGrant(
			operatorAddress,
			aiOperationalAddress,
			authorization,
			&expirationTime,
		)
		if err != nil {
			return fmt.Errorf("failed to create MsgGrant for %s: %w", sdk.MsgTypeURL(msgType), err)
		}
		grantMsgs = append(grantMsgs, grantMsg)
	}

	txb, err := txFactory.BuildUnsignedTx(grantMsgs...)
	if err != nil {
		return err
	}

	err = tx.Sign(ctx, txFactory, clientCtx.GetFromName(), txb, true)
	if err != nil {
		return err
	}

	txBytes, err := clientCtx.TxConfig.TxEncoder()(txb.GetTx())
	if err != nil {
		return err
	}

	res, err := clientCtx.BroadcastTx(txBytes)
	if err != nil {
		return err
	}

	if res.Code != 0 {
		return fmt.Errorf("transaction failed on broadcast with code %d: %s", res.Code, res.RawLog)
	}

	txHash := res.TxHash
	fmt.Printf("Transaction sent with hash: %s\n", txHash)
	fmt.Println("Waiting for transaction to be included in a block...")

	for i := 0; i < 20; i++ {
		time.Sleep(3 * time.Second)

		txHashBytes, hexErr := hex.DecodeString(txHash)
		if hexErr != nil {
			return fmt.Errorf("failed to decode transaction hash: %w", hexErr)
		}

		txResponse, err := clientCtx.Client.Tx(ctx, txHashBytes, false)

		if err != nil {
			fmt.Print(".")
			continue
		}

		if txResponse.Height > 0 {
			if txResponse.TxResult.Code == 0 {
				fmt.Println("\nTransaction confirmed successfully!")
				fmt.Printf("Block height: %d\n", txResponse.Height)
				return nil
			} else {
				return fmt.Errorf("\nTransaction %s included in block %d but failed with code %d: %s", txHash, txResponse.Height, txResponse.TxResult.Code, txResponse.TxResult.Log)
			}
		}

		fmt.Print("+")
	}

	return fmt.Errorf("\nTimed out waiting for transaction %s to be confirmed in a block", txHash)
}
