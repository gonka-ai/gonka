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
// cold to warm key. Paid PoC store commits spend it; rerun the grant command
// to refresh it.
var DefaultMLOpsFeeAllowance = sdk.NewCoins(sdk.NewCoin("ngonka", sdkmath.NewInt(10_000_000_000))) // 10 GNK

var InferenceOperationKeyPerms = []sdk.Msg{
	&types.MsgClaimRewards{},
	&types.MsgSubmitPocBatch{},
	&types.MsgSubmitPocValidationsV2{},        // PoC v2 validations
	&types.MsgPoCV2StoreCommit{},              // PoC v2 off-chain store commits
	&types.MsgSubmitPoCChallengeValidations{}, // PoC challenge validations
	&types.MsgPoCChallengeStoreCommit{},       // PoC challenge store commits
	&types.MsgMLNodeWeightDistribution{},      // PoC v2 ML node weight distribution
	&types.MsgSubmitSeed{},
	&types.MsgBridgeExchange{},
	&types.MsgSubmitNewUnfundedParticipant{},
	&types.MsgSubmitHardwareDiff{},
	&types.MsgDeclarePoCIntent{},         // bootstrap-model intent; miners submit via warm key
	&types.MsgRefreshTrainingNodeOptIn{}, // the training daemon offers the host's nodes
	&types.MsgAutokickTrainshardNode{},   // and hands one back when its run fails
	&blstypes.MsgSubmitDealerPart{},
	&blstypes.MsgSubmitVerificationVector{},
	&blstypes.MsgRespondDealerComplaints{},
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
	// The chain rejects a duplicate MsgGrantAllowance, so an existing
	// allowance is kept unless it is expired, expires before the new authz
	// grants or holds less than the default; then it is revoked and granted
	// again in this tx, so rerunning this command refreshes it.
	existing, err := queryFeegrantAllowance(ctx, clientCtx, operatorAddress, aiOperationalAddress)
	if err != nil {
		fmt.Printf("Warning: could not check existing feegrant allowance: %v\n", err)
		// Continue and let the chain reject the duplicate if necessary.
	}
	refresh, spendLimit, allowanceExp := feegrantRefresh(existing, time.Now(), expirationTime)
	if existing == nil || refresh {
		if refresh {
			revokeMsg := feegrant.NewMsgRevokeAllowance(operatorAddress, aiOperationalAddress)
			grantMsgs = append(grantMsgs, &revokeMsg)
		}
		allowance := &feegrant.BasicAllowance{
			SpendLimit: spendLimit,
			Expiration: allowanceExp,
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
		if refresh {
			fmt.Println("Refreshing the feegrant allowance from cold to warm in this transaction.")
		} else {
			fmt.Println("Including new feegrant allowance from cold to warm in this transaction.")
		}
	} else {
		fmt.Println("Existing feegrant allowance from cold to warm is valid; skipping MsgGrantAllowance.")
	}

	// This command bypasses GenerateOrBroadcastTxCLI, so we replicate its
	// --gas auto handling here: when the factory is set to simulate, run
	// CalculateGas and apply the adjustment before building the tx. Without
	// this, --gas auto produces a tx with gasWanted=0 and OOGs immediately.
	if txFactory.SimulateAndExecute() {
		_, adjusted, err := tx.CalculateGas(clientCtx, txFactory, grantMsgs...)
		if err != nil {
			return fmt.Errorf("failed to simulate gas: %w", err)
		}
		txFactory = txFactory.WithGas(adjusted)
		fmt.Printf("gas estimate: %d\n", adjusted)
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

// queryFeegrantAllowance returns the allowance from granter to grantee, or
// nil when there is none. Any other error is propagated.
func queryFeegrantAllowance(ctx context.Context, clientCtx client.Context, granter, grantee sdk.AccAddress) (feegrant.FeeAllowanceI, error) {
	queryClient := feegrant.NewQueryClient(clientCtx)
	resp, err := queryClient.Allowance(ctx, &feegrant.QueryAllowanceRequest{
		Granter: granter.String(),
		Grantee: grantee.String(),
	})
	if err != nil {
		// "not found" is the expected case for new hosts; treat it as "no allowance".
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "NotFound") {
			return nil, nil
		}
		return nil, err
	}
	if resp == nil || resp.Allowance == nil || resp.Allowance.Allowance == nil {
		return nil, nil
	}
	var allowance feegrant.FeeAllowanceI
	if err := clientCtx.Codec.UnpackAny(resp.Allowance.Allowance, &allowance); err != nil {
		return nil, err
	}
	return allowance, nil
}

// feegrantRefresh reports whether an existing BasicAllowance must be
// replaced, and the spend limit and expiration for the new one. A larger or
// unlimited limit and a later or absent expiration are kept; other allowance
// types are left to the operator.
func feegrantRefresh(existing feegrant.FeeAllowanceI, now, expiration time.Time) (bool, sdk.Coins, *time.Time) {
	basic, ok := existing.(*feegrant.BasicAllowance)
	if !ok {
		return false, DefaultMLOpsFeeAllowance, &expiration
	}
	const denom = "ngonka"
	spendLimit := DefaultMLOpsFeeAllowance
	low := false
	if !basic.SpendLimit.IsZero() {
		low = basic.SpendLimit.AmountOf(denom).LT(DefaultMLOpsFeeAllowance.AmountOf(denom))
		if !low {
			spendLimit = basic.SpendLimit
		}
	} else {
		spendLimit = nil
	}
	exp := &expiration
	expiring := false
	if basic.Expiration == nil {
		exp = nil
	} else if basic.Expiration.After(expiration) {
		exp = basic.Expiration
	} else {
		expiring = true
	}
	return low || expiring, spendLimit, exp
}
