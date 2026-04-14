package inference

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	sdkmath "cosmossdk.io/math"
	"cosmossdk.io/x/feegrant"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/tx"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/types"
	// this line is used by starport scaffolding # 1
)

// DefaultMLOpsFeeAllowance is the spend limit on the feegrant allowance from
// cold to warm key when granting ML ops permissions. At ~10 ngonka per gas
// and typical transaction sizes, this covers many months of routine DAPI
// operation (claim rewards, hardware diff updates, seeds). Hosts can re-grant
// when the allowance is depleted.
var DefaultMLOpsFeeAllowance = sdk.NewCoins(sdk.NewCoin("ngonka", sdkmath.NewInt(10_000_000_000))) // 10 GNK

var InferenceOperationKeyPerms = []sdk.Msg{
	&types.MsgStartInference{},
	&types.MsgFinishInference{},
	&types.MsgClaimRewards{},
	&types.MsgValidation{},
	&types.MsgSubmitPocBatch{},
	&types.MsgSubmitPocValidationsV2{},   // PoC v2 validations
	&types.MsgPoCV2StoreCommit{},         // PoC v2 off-chain store commits
	&types.MsgMLNodeWeightDistribution{}, // PoC v2 ML node weight distribution
	&types.MsgSubmitSeed{},
	&types.MsgBridgeExchange{},
	&types.MsgSubmitNewUnfundedParticipant{},
	&types.MsgSubmitHardwareDiff{},
	&types.MsgInvalidateInference{},
	&types.MsgRevalidateInference{},
	&blstypes.MsgSubmitDealerPart{},
	&blstypes.MsgSubmitVerificationVector{},
	&blstypes.MsgRespondDealerComplaints{},
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

	// Also grant a fee allowance from cold to warm so the warm key can pay
	// transaction fees on behalf of the cold account. The DAPI sets the cold
	// account as the fee_granter on every tx; without this allowance, the
	// chain rejects the tx because the warm key has no balance.
	//
	// We check for an existing allowance first because the chain rejects
	// duplicate MsgGrantAllowance with "fee allowance already exists". Hosts
	// who upgraded from v0.2.11 already have an allowance auto-created by the
	// v0.2.12 upgrade handler; in that case we skip this message and only
	// re-grant the authz permissions. To refresh an expired or depleted
	// allowance, hosts must first run `inferenced tx feegrant revoke`.
	hasExistingAllowance, err := checkFeegrantExists(ctx, clientCtx, operatorAddress, aiOperationalAddress)
	if err != nil {
		fmt.Printf("Warning: could not check existing feegrant allowance: %v\n", err)
		// Continue and let the chain reject the duplicate if necessary.
	}
	if !hasExistingAllowance {
		allowance := &feegrant.BasicAllowance{
			SpendLimit: DefaultMLOpsFeeAllowance,
			Expiration: &expirationTime,
		}
		feeGrantMsg, err := feegrant.NewMsgGrantAllowance(
			allowance,
			operatorAddress,
			aiOperationalAddress,
		)
		if err != nil {
			return fmt.Errorf("failed to create MsgGrantAllowance: %w", err)
		}
		grantMsgs = append(grantMsgs, feeGrantMsg)
		fmt.Println("Including new feegrant allowance from cold to warm in this transaction.")
	} else {
		fmt.Println("Existing feegrant allowance from cold to warm detected; skipping MsgGrantAllowance. " +
			"Run `inferenced tx feegrant revoke <warm-address>` first if you want to refresh it.")
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

// checkFeegrantExists queries the chain for an existing feegrant allowance
// from granter to grantee. Returns true if one exists, false if not (including
// when the query returns "not found"). Any other error is propagated.
func checkFeegrantExists(ctx context.Context, clientCtx client.Context, granter, grantee sdk.AccAddress) (bool, error) {
	queryClient := feegrant.NewQueryClient(clientCtx)
	resp, err := queryClient.Allowance(ctx, &feegrant.QueryAllowanceRequest{
		Granter: granter.String(),
		Grantee: grantee.String(),
	})
	if err != nil {
		// "not found" is the expected case for new hosts; treat it as "no allowance".
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "NotFound") {
			return false, nil
		}
		return false, err
	}
	return resp != nil && resp.Allowance != nil, nil
}
