package inference

import (
	"context"
	"fmt"
	"time"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/tx"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
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
	&types.MsgSubmitNewParticipant{},
	&types.MsgSubmitHardwareDiff{},
	&types.MsgInvalidateInference{},
	&types.MsgRevalidateInference{},
}

func GrantOperationKeyPermissionsToAccount(
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

	var grantMsgs []sdk.Msg

	var expirationTime time.Time
	if expiration != nil {
		expirationTime = *expiration
	} else {
		expirationTime = time.Now().Add(365 * 24 * time.Hour) // 1 year
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

	// Use the standard CLI transaction flow instead of manual building
	return tx.GenerateOrBroadcastTxWithFactory(clientCtx, txFactory, grantMsgs...)
}
