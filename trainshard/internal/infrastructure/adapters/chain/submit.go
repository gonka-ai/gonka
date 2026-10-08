package chain

import (
	"context"
	"encoding/hex"
	"fmt"
	"slices"
	"time"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/client"
	clienttx "github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	vestingtypes "github.com/cosmos/cosmos-sdk/x/auth/vesting/types"
	authz "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

const (
	denom       = "ngonka"
	gasFallback = 400_000
)

// Key is the account a coordinator drives its run from: unlike a host, it signs for itself
type Key interface {
	Address() vo.Address
	Account() cryptotypes.PrivKey
}

const (
	landingDefault = 2 * time.Minute
	blockTime      = 5 * time.Second
)

var accountTypes = accountRegistry()

type Signer struct {
	*Client
	key      Key
	accounts authtypes.QueryClient
	sender   txtypes.ServiceClient
	config   client.TxConfig
	chainID  string
	landing  time.Duration
}

func NewSigner(client *Client, key Key, chainID string, landing time.Duration) *Signer {
	if landing <= 0 {
		landing = landingDefault
	}
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	authtypes.RegisterInterfaces(registry)
	types.RegisterInterfaces(registry)

	return &Signer{
		Client:   client,
		key:      key,
		accounts: authtypes.NewQueryClient(client.conn),
		sender:   txtypes.NewServiceClient(client.conn),
		config:   authtx.NewTxConfig(codec.NewProtoCodec(registry), []signingtypes.SignMode{signingtypes.SignMode_SIGN_MODE_DIRECT}),
		chainID:  chainID,
		landing:  landing,
	}
}

func (s *Signer) OptIn(context.Context, vo.NodeRef, time.Duration) error {
	return shared.New("NOT_A_HOST", shared.ErrConflict, "a coordinator has no nodes of its own to offer for a shard")
}

// the request id is derived, not random, so a retry of a release that already landed is a no-op on chain
func (s *Signer) Release(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, reason vo.ReleaseReason) error {
	return s.send(ctx, &types.MsgAutokickTrainshardNode{
		Creator:      string(s.key.Address()),
		TrainshardId: uint64(shardID),
		Participant:  string(node.Participant),
		NodeId:       string(node.NodeID),
		Reason:       string(reason),
		RequestId:    fmt.Sprintf("release/%s/%s/%s/%s", shardID, node.Participant, node.NodeID, reason),
	})
}

func (s *Signer) AssemblyOpensAt(ctx context.Context) (vo.Height, vo.Height, error) {
	now, opens, _, err := s.assemblyWindow(ctx)
	return now, opens, err
}

// the transaction expires in the block before the next PoC, so a regular PoC never refuses it; one
// that expired is sent back to waiting, it can no longer run. The chain names the new shard only in
// its answer to this transaction
func (s *Signer) Assemble(ctx context.Context, proposal uint64) (vo.ShardID, error) {
	at, opens, closes, err := s.assemblyWindow(ctx)
	if err != nil {
		return 0, err
	}
	if at < opens {
		return 0, fmt.Errorf("assembly opens at height %d, now %d: %w", opens, at, shard.ErrAssemblyClosed)
	}
	timeout := min(at+vo.Height(s.blocksToLand()), closes-1)
	answer, err := s.submitUntil(ctx, &types.MsgAssembleTrainshard{Creator: string(s.key.Address()), ProposalId: proposal}, timeout)
	if shared.CodeOf(err) == codeExpired {
		return 0, fmt.Errorf("%w: %w", err, shard.ErrAssemblyClosed)
	}
	if err != nil {
		return 0, err
	}
	var assembled types.MsgAssembleTrainshardResponse
	if err := response(answer, &assembled); err != nil {
		return 0, err
	}
	return vo.ShardID(assembled.TrainshardId), nil
}

func (s *Signer) Settle(ctx context.Context, shardID vo.ShardID) error {
	return s.send(ctx, &types.MsgSettleTrainshard{
		Creator:      string(s.key.Address()),
		TrainshardId: uint64(shardID),
	})
}

func (s *Signer) send(ctx context.Context, msg sdk.Msg) error {
	_, err := s.submit(ctx, msg)
	return err
}

// no fixed gas covers every message: an assemble and a settle grow with the nodes in the shard
func (s *Signer) gas(ctx context.Context, builder client.TxBuilder) (uint64, error) {
	encoded, err := s.config.TxEncoder()(builder.GetTx())
	if err != nil {
		return 0, err
	}
	answer, err := s.sender.Simulate(ctx, &txtypes.SimulateRequest{TxBytes: encoded})
	if err != nil {
		return gasFallback, nil
	}
	return answer.GasInfo.GasUsed * 3 / 2, nil
}

func response(answer *sdk.TxResponse, out interface{ Unmarshal([]byte) error }) error {
	raw, err := hex.DecodeString(answer.Data)
	if err != nil {
		return err
	}
	var data sdk.TxMsgData
	if err := data.Unmarshal(raw); err != nil {
		return err
	}
	if len(data.MsgResponses) == 0 {
		return shared.New("CHAIN_SILENT", shared.ErrUnavailable, "the chain ran the message and said nothing back")
	}
	return out.Unmarshal(data.MsgResponses[0].Value)
}

func (s *Signer) submit(ctx context.Context, msg sdk.Msg) (*sdk.TxResponse, error) {
	at, err := s.Height(ctx)
	if err != nil {
		return nil, err
	}
	return s.submitUntil(ctx, msg, at+vo.Height(s.blocksToLand()))
}

// submitUntil lets the transaction land up to the timeout height and never later
func (s *Signer) submitUntil(ctx context.Context, msg sdk.Msg, timeout vo.Height) (*sdk.TxResponse, error) {
	number, sequence, err := s.account(ctx)
	if err != nil {
		return nil, err
	}

	builder := s.config.NewTxBuilder()
	if err := builder.SetMsgs(msg); err != nil {
		return nil, err
	}
	builder.SetTimeoutHeight(uint64(timeout))
	// what is signed covers who signs it, so the key and the sequence go in before the signature that
	// then replaces this blank one
	blank := signingtypes.SignatureV2{
		PubKey:   s.key.Account().PubKey(),
		Data:     &signingtypes.SingleSignatureData{SignMode: signingtypes.SignMode_SIGN_MODE_DIRECT},
		Sequence: sequence,
	}
	if err := builder.SetSignatures(blank); err != nil {
		return nil, err
	}

	limit, err := s.gas(ctx, builder)
	if err != nil {
		return nil, err
	}
	builder.SetGasLimit(limit)
	builder.SetFeeAmount(sdk.NewCoins(sdk.NewCoin(denom, math.NewInt(int64(limit)*s.price(ctx)))))

	signature, err := clienttx.SignWithPrivKey(ctx, signingtypes.SignMode_SIGN_MODE_DIRECT,
		authsigning.SignerData{ChainID: s.chainID, AccountNumber: number, Sequence: sequence},
		builder, s.key.Account(), s.config, sequence)
	if err != nil {
		return nil, err
	}
	if err := builder.SetSignatures(signature); err != nil {
		return nil, err
	}

	encoded, err := s.config.TxEncoder()(builder.GetTx())
	if err != nil {
		return nil, err
	}
	answer, err := s.sender.BroadcastTx(ctx, &txtypes.BroadcastTxRequest{
		TxBytes: encoded,
		Mode:    txtypes.BroadcastMode_BROADCAST_MODE_SYNC,
	})
	if err != nil {
		return nil, unreachable(err)
	}
	if answer.TxResponse.Code != 0 {
		return nil, Refused(msg, answer.TxResponse.Codespace, answer.TxResponse.Code, answer.TxResponse.RawLog)
	}
	return s.landed(ctx, msg, answer.TxResponse.TxHash, sequence, timeout)
}

// a broadcast is answered when the chain takes the transaction, not when it runs it: without this wait
// the next one signs with a stale sequence and a message the chain then refused reads as done
func (s *Signer) landed(ctx context.Context, msg sdk.Msg, hash string, sequence uint64, timeout vo.Height) (*sdk.TxResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, s.landing)
	defer cancel()

	var last error
	for {
		answer, err := s.sender.GetTx(ctx, &txtypes.GetTxRequest{Hash: hash})
		switch {
		case err == nil && answer.TxResponse.Code != 0:
			return nil, Refused(msg, answer.TxResponse.Codespace, answer.TxResponse.Code, answer.TxResponse.RawLog)
		case err == nil:
			return answer.TxResponse, nil
		case status.Code(err) != codes.NotFound:
			last = err
		default:
			if s.expiredUnrun(ctx, sequence, timeout) {
				return nil, expired(msg, hash, timeout)
			}
		}
		select {
		case <-ctx.Done():
			return nil, slow(msg, hash, s.landing, last)
		case <-time.After(s.poll):
		}
	}
}

func (s *Signer) blocksToLand() uint64 {
	return uint64(max(1, s.landing/blockTime)) + 1
}

func slow(msg sdk.Msg, hash string, waited time.Duration, last error) error {
	reason := fmt.Sprintf("the chain took %s as %s and did not run it within %s", sdk.MsgTypeURL(msg), hash, waited)
	if last != nil {
		reason += ": " + last.Error()
	}
	return shared.New("CHAIN_SLOW", shared.ErrUnavailable, reason)
}

// the transaction index may lag the blocks, the account sequence is chain state: still at the
// transaction's own past its timeout height, it never ran and no longer can
func (s *Signer) expiredUnrun(ctx context.Context, sequence uint64, timeout vo.Height) bool {
	var stamp metadata.MD
	_, next, err := s.account(ctx, grpc.Header(&stamp))
	if err != nil || next > sequence {
		return false
	}
	at, err := height(stamp)
	return err == nil && at > timeout
}

const codeExpired = "CHAIN_EXPIRED"

func expired(msg sdk.Msg, hash string, timeout vo.Height) error {
	return shared.New(codeExpired, shared.ErrUnavailable,
		fmt.Sprintf("the chain took %s as %s and passed its timeout height %d without running it", sdk.MsgTypeURL(msg), hash, timeout))
}

// Refused reads a refusal by its codespace and code alone, never by its log, whose wording is no contract.
// Out of gas and a low fee stay unavailable: every submit simulates gas and reads the price again.
func Refused(msg sdk.Msg, codespace string, code uint32, log string) error {
	reason := fmt.Sprintf("the chain refused %s with code %d in codespace %q: %s", sdk.MsgTypeURL(msg), code, codespace, log)
	if among(codespace, code, types.ErrTrainshardAssemblyDuringPoC) {
		return fmt.Errorf("%s: %w", reason, shard.ErrAssemblyClosed)
	}
	return shared.New("CHAIN_REFUSED", refusal(codespace, code), reason)
}

func refusal(codespace string, code uint32) error {
	switch {
	case codespace == types.ModuleName:
		return shared.ErrConflict
	case among(codespace, code,
		sdkerrors.ErrInsufficientFunds,
		sdkerrors.ErrUnauthorized,
		sdkerrors.ErrInvalidAddress,
		sdkerrors.ErrInvalidRequest,
		sdkerrors.ErrUnknownRequest,
		sdkerrors.ErrTxDecode,
		sdkerrors.ErrInvalidPubKey,
		sdkerrors.ErrInvalidCoins,
		sdkerrors.ErrNoSignatures,
		sdkerrors.ErrTooManySignatures,
		authz.ErrNoAuthorizationFound,
		authz.ErrAuthorizationExpired,
	):
		return shared.ErrConflict
	default:
		return shared.ErrUnavailable
	}
}

func among(codespace string, code uint32, known ...*errorsmod.Error) bool {
	return slices.ContainsFunc(known, func(e *errorsmod.Error) bool {
		return e.Codespace() == codespace && e.ABCICode() == code
	})
}

func (s *Signer) account(ctx context.Context, opts ...grpc.CallOption) (number, sequence uint64, err error) {
	answer, err := s.accounts.Account(ctx, &authtypes.QueryAccountRequest{Address: string(s.key.Address())}, opts...)
	if status.Code(err) == codes.NotFound {
		return 0, 0, shared.New("ACCOUNT_UNKNOWN", shared.ErrNotFound,
			fmt.Sprintf("the chain holds no account for %s: it needs funds before it can sign", s.key.Address()))
	}
	if err != nil {
		return 0, 0, unreachable(err)
	}
	var held sdk.AccountI
	if err := accountTypes.UnpackAny(answer.Account, &held); err != nil || held == nil {
		return 0, 0, shared.New("ACCOUNT_UNREADABLE", shared.ErrConflict,
			fmt.Sprintf("the chain holds %s as %q, an account type this coordinator cannot sign for", s.key.Address(), answer.Account.GetTypeUrl()))
	}
	return held.GetAccountNumber(), held.GetSequence(), nil
}

// the chain hands out a vesting account as its own type, with the base account nested inside
func accountRegistry() codectypes.InterfaceRegistry {
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	authtypes.RegisterInterfaces(registry)
	vestingtypes.RegisterInterfaces(registry)
	return registry
}

// a chain that does not say its gas price is taken to charge nothing
func (s *Signer) price(ctx context.Context) int64 {
	answer, err := s.query.Params(ctx, &types.QueryParamsRequest{})
	if err != nil || answer.Params.FeeParams == nil {
		return 0
	}
	return int64(answer.Params.FeeParams.MinGasPriceNgonka)
}
