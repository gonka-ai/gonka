package chain

import (
	"context"
	"errors"
	"testing"
	"time"

	errorsmod "cosmossdk.io/errors"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cosmossecp "github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	vestingtypes "github.com/cosmos/cosmos-sdk/x/auth/vesting/types"
	authz "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/cosmos/gogoproto/proto"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

type senderStub struct {
	txtypes.ServiceClient
	answers []error
	ran     sdk.TxResponse
	asked   int
}

func (s *senderStub) GetTx(context.Context, *txtypes.GetTxRequest, ...grpc.CallOption) (*txtypes.GetTxResponse, error) {
	s.asked++
	if s.asked > len(s.answers) {
		return &txtypes.GetTxResponse{TxResponse: &s.ran}, nil
	}
	return nil, s.answers[s.asked-1]
}

type accountsStub struct {
	authtypes.QueryClient
	held *codectypes.Any
	err  error
}

func (a accountsStub) Account(context.Context, *authtypes.QueryAccountRequest, ...grpc.CallOption) (*authtypes.QueryAccountResponse, error) {
	if a.err != nil {
		return nil, a.err
	}
	return &authtypes.QueryAccountResponse{Account: a.held}, nil
}

type keyStub struct{}

func (keyStub) Address() vo.Address          { return "gonka1creator" }
func (keyStub) Account() cryptotypes.PrivKey { return cosmossecp.GenPrivKey() }

func signerOver(sender *senderStub, landing time.Duration) *Signer {
	return &Signer{Client: &Client{poll: time.Millisecond}, sender: sender, landing: landing}
}

func TestLandedWaitsThroughNotFoundUntilTheBlockRuns(t *testing.T) {
	// arrange
	notFound := status.Error(codes.NotFound, "tx not found")
	sender := &senderStub{answers: []error{notFound, notFound, errors.New("blip")}}

	// act
	answer, err := signerOver(sender, time.Second).landed(context.Background(), &types.MsgSettleTrainshard{}, "ABCD")

	// assert
	if err != nil || answer == nil {
		t.Fatalf("got %v, %v", answer, err)
	}
	if sender.asked != 4 {
		t.Fatalf("asked %d times", sender.asked)
	}
}

func TestLandedGivesUpAfterTheLandingWindow(t *testing.T) {
	// arrange
	notFound := status.Error(codes.NotFound, "tx not found")
	sender := &senderStub{answers: make([]error, 0)}
	for range 100000 {
		sender.answers = append(sender.answers, notFound)
	}

	// act
	_, err := signerOver(sender, 20*time.Millisecond).landed(context.Background(), &types.MsgSettleTrainshard{}, "ABCD")

	// assert
	if shared.CodeOf(err) != "CHAIN_SLOW" {
		t.Fatalf("got %v, want CHAIN_SLOW", err)
	}
}

func TestBlocksToLandCoverTheLandingWindow(t *testing.T) {
	// act
	long, short := signerOver(nil, 2*time.Minute).blocksToLand(), signerOver(nil, time.Second).blocksToLand()

	// assert
	if long != 25 || short != 2 {
		t.Fatalf("got %d and %d blocks, want 25 and 2", long, short)
	}
}

func TestAnAccountIsUnknownOnlyWhenTheChainSaysSo(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantKind error
		wantCode string
	}{
		{"the chain holds no such account", status.Error(codes.NotFound, "account not found"), shared.ErrNotFound, "ACCOUNT_UNKNOWN"},
		{"the chain cannot be reached", status.Error(codes.Unavailable, "connection refused"), shared.ErrUnavailable, "CHAIN_UNREACHABLE"},
		{"the chain gave up on the call", status.Error(codes.DeadlineExceeded, "deadline exceeded"), shared.ErrUnavailable, "CHAIN_UNREACHABLE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// arrange
			signer := &Signer{key: keyStub{}, accounts: accountsStub{err: tc.err}}

			// act
			_, _, err := signer.account(context.Background())

			// assert
			if !errors.Is(err, tc.wantKind) || shared.CodeOf(err) != tc.wantCode {
				t.Fatalf("got %v (%s), want %s", err, shared.CodeOf(err), tc.wantCode)
			}
		})
	}
}

func onWire(t *testing.T, held proto.Message) *codectypes.Any {
	t.Helper()
	packed, err := codectypes.NewAnyWithValue(held)
	if err != nil {
		t.Fatal(err)
	}
	return &codectypes.Any{TypeUrl: packed.TypeUrl, Value: packed.Value}
}

func TestAnAccountIsReadWhicheverTypeTheChainHoldsItAs(t *testing.T) {
	key := cosmossecp.GenPrivKey().PubKey()
	base := authtypes.NewBaseAccount(sdk.AccAddress(key.Address()), key, 7, 3)
	vesting := &vestingtypes.ContinuousVestingAccount{
		BaseVestingAccount: &vestingtypes.BaseVestingAccount{
			BaseAccount:     authtypes.NewBaseAccount(sdk.AccAddress(key.Address()), key, 9, 4),
			OriginalVesting: sdk.NewCoins(sdk.NewInt64Coin(denom, 1000)),
			EndTime:         2_000_000_000,
		},
		StartTime: 1_000_000_000,
	}
	cases := []struct {
		name                     string
		held                     *codectypes.Any
		wantNumber, wantSequence uint64
		wantCode                 string
	}{
		{"a base account", onWire(t, base), 7, 3, ""},
		{"a continuous vesting account nesting its base account", onWire(t, vesting), 9, 4, ""},
		{"an account type the coordinator does not know", onWire(t, &types.MsgSettleTrainshard{}), 0, 0, "ACCOUNT_UNREADABLE"},
		{"an answer that carries no account", nil, 0, 0, "ACCOUNT_UNREADABLE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// arrange
			signer := &Signer{key: keyStub{}, accounts: accountsStub{held: tc.held}}

			// act
			number, sequence, err := signer.account(context.Background())

			// assert
			if tc.wantCode != "" {
				if !errors.Is(err, shared.ErrConflict) || shared.CodeOf(err) != tc.wantCode {
					t.Fatalf("got %v (%s), want %s", err, shared.CodeOf(err), tc.wantCode)
				}
				return
			}
			if err != nil || number != tc.wantNumber || sequence != tc.wantSequence {
				t.Fatalf("got number %d, sequence %d, %v; want %d, %d", number, sequence, err, tc.wantNumber, tc.wantSequence)
			}
		})
	}
}

func TestARefusalIsFinalOnlyWhenItsCodespaceAndCodeSaySo(t *testing.T) {
	cases := []struct {
		name      string
		codespace string
		code      uint32
		wantKind  error
	}{
		{"the inference module: only the creator may do this", types.ModuleName, types.ErrTrainshardNotCreator.ABCICode(), shared.ErrConflict},
		{"the inference module: the shard is not active", types.ModuleName, types.ErrTrainshardNotActive.ABCICode(), shared.ErrConflict},
		{"insufficient funds", sdkerrors.RootCodespace, sdkerrors.ErrInsufficientFunds.ABCICode(), shared.ErrConflict},
		{"unauthorized signer or permission", sdkerrors.RootCodespace, sdkerrors.ErrUnauthorized.ABCICode(), shared.ErrConflict},
		{"invalid address", sdkerrors.RootCodespace, sdkerrors.ErrInvalidAddress.ABCICode(), shared.ErrConflict},
		{"invalid request", sdkerrors.RootCodespace, sdkerrors.ErrInvalidRequest.ABCICode(), shared.ErrConflict},
		{"unknown request", sdkerrors.RootCodespace, sdkerrors.ErrUnknownRequest.ABCICode(), shared.ErrConflict},
		{"tx decode failed", sdkerrors.RootCodespace, sdkerrors.ErrTxDecode.ABCICode(), shared.ErrConflict},
		{"invalid public key", sdkerrors.RootCodespace, sdkerrors.ErrInvalidPubKey.ABCICode(), shared.ErrConflict},
		{"invalid coins", sdkerrors.RootCodespace, sdkerrors.ErrInvalidCoins.ABCICode(), shared.ErrConflict},
		{"no signatures", sdkerrors.RootCodespace, sdkerrors.ErrNoSignatures.ABCICode(), shared.ErrConflict},
		{"too many signatures", sdkerrors.RootCodespace, sdkerrors.ErrTooManySignatures.ABCICode(), shared.ErrConflict},
		{"authz authorization missing", authz.ModuleName, authz.ErrNoAuthorizationFound.ABCICode(), shared.ErrConflict},
		{"authz authorization expired", authz.ModuleName, authz.ErrAuthorizationExpired.ABCICode(), shared.ErrConflict},
		{"account sequence mismatch", sdkerrors.RootCodespace, sdkerrors.ErrWrongSequence.ABCICode(), shared.ErrUnavailable},
		{"mempool full", sdkerrors.RootCodespace, sdkerrors.ErrMempoolIsFull.ABCICode(), shared.ErrUnavailable},
		{"already in the mempool", sdkerrors.RootCodespace, sdkerrors.ErrTxInMempoolCache.ABCICode(), shared.ErrUnavailable},
		{"timeout height passed", sdkerrors.RootCodespace, sdkerrors.ErrTxTimeoutHeight.ABCICode(), shared.ErrUnavailable},
		{"timeout timestamp passed", sdkerrors.RootCodespace, sdkerrors.ErrTxTimeout.ABCICode(), shared.ErrUnavailable},
		{"out of gas, simulated again on the retry", sdkerrors.RootCodespace, sdkerrors.ErrOutOfGas.ABCICode(), shared.ErrUnavailable},
		{"insufficient fee, priced again on the retry", sdkerrors.RootCodespace, sdkerrors.ErrInsufficientFee.ABCICode(), shared.ErrUnavailable},
		{"an sdk code not named", sdkerrors.RootCodespace, 9999, shared.ErrUnavailable},
		{"another module's unnamed code", authz.ModuleName, 9999, shared.ErrUnavailable},
		{"an inference code with no codespace", "", types.ErrTrainshardNotCreator.ABCICode(), shared.ErrUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// act
			err := Refused(&types.MsgSettleTrainshard{}, tc.codespace, tc.code, "whatever the log says")

			// assert
			if !errors.Is(err, tc.wantKind) || shared.CodeOf(err) != "CHAIN_REFUSED" {
				t.Fatalf("got %v (%s), want %v", err, shared.CodeOf(err), tc.wantKind)
			}
		})
	}
}

func TestLandedKeepsTheCodespaceOfARefusalThatRan(t *testing.T) {
	// arrange
	sender := &senderStub{ran: refusedBy(types.ErrTrainshardNotCreator)}

	// act
	_, err := signerOver(sender, time.Second).landed(context.Background(), &types.MsgSettleTrainshard{}, "ABCD")

	// assert
	if !errors.Is(err, shared.ErrConflict) || shared.CodeOf(err) != "CHAIN_REFUSED" {
		t.Fatalf("got %v (%s), want a final CHAIN_REFUSED", err, shared.CodeOf(err))
	}
}

func refusedBy(e *errorsmod.Error) sdk.TxResponse {
	return sdk.TxResponse{Codespace: e.Codespace(), Code: e.ABCICode(), RawLog: e.Error()}
}
