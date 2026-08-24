package chain

import (
	"context"
	"fmt"
	"time"

	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/client"
	clienttx "github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/productscience/inference/x/inference/types"

	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

const (
	denom = "ngonka"
	gas   = 400_000
)

// Key is the account a coordinator drives its run from: unlike a host, it signs for itself
type Key interface {
	Address() vo.Address
	Account() cryptotypes.PrivKey
}

// Signer submits what the shard's own actor is allowed to ask of the chain
type Signer struct {
	*Client
	key      Key
	accounts authtypes.QueryClient
	sender   txtypes.ServiceClient
	config   client.TxConfig
	chainID  string
}

func NewSigner(client *Client, key Key, chainID string) *Signer {
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
	}
}

// OptIn is a host's offer to lend a node, which a coordinator never makes
func (s *Signer) OptIn(context.Context, vo.NodeRef, time.Duration) error {
	return shared.New("NOT_A_HOST", shared.ErrConflict, "a coordinator lends no nodes of its own")
}

// Release drops the node from the run. Whoever the shard answers to may ask, and the id is the same
// on every retry so a repeat of one that already landed changes nothing
func (s *Signer) Release(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, reason vo.ReleaseReason) error {
	return s.send(ctx, &types.MsgAutokickTrainshardNode{
		Creator:      string(s.key.Address()),
		TrainshardId: uint64(shardID),
		Participant:  string(node.Participant),
		NodeId:       string(node.NodeID),
		Reason:       string(reason),
		RequestId:    fmt.Sprintf("release/%s/%s/%s", shardID, node.NodeID, reason),
	})
}

func (s *Signer) send(ctx context.Context, msg sdk.Msg) error {
	number, sequence, err := s.account(ctx)
	if err != nil {
		return err
	}

	builder := s.config.NewTxBuilder()
	if err := builder.SetMsgs(msg); err != nil {
		return err
	}
	builder.SetGasLimit(gas)
	builder.SetFeeAmount(sdk.NewCoins(sdk.NewCoin(denom, math.NewInt(gas*s.price(ctx)))))

	// what is signed covers who signs it, so the key and the sequence go in before the signature that
	// then replaces this blank one
	blank := signingtypes.SignatureV2{
		PubKey:   s.key.Account().PubKey(),
		Data:     &signingtypes.SingleSignatureData{SignMode: signingtypes.SignMode_SIGN_MODE_DIRECT},
		Sequence: sequence,
	}
	if err := builder.SetSignatures(blank); err != nil {
		return err
	}

	signature, err := clienttx.SignWithPrivKey(ctx, signingtypes.SignMode_SIGN_MODE_DIRECT,
		authsigning.SignerData{ChainID: s.chainID, AccountNumber: number, Sequence: sequence},
		builder, s.key.Account(), s.config, sequence)
	if err != nil {
		return err
	}
	if err := builder.SetSignatures(signature); err != nil {
		return err
	}

	encoded, err := s.config.TxEncoder()(builder.GetTx())
	if err != nil {
		return err
	}
	answer, err := s.sender.BroadcastTx(ctx, &txtypes.BroadcastTxRequest{
		TxBytes: encoded,
		Mode:    txtypes.BroadcastMode_BROADCAST_MODE_SYNC,
	})
	if err != nil {
		return shared.New("CHAIN_UNREACHABLE", shared.ErrUnavailable, err.Error())
	}
	if answer.TxResponse.Code != 0 {
		return refused(msg, answer.TxResponse.Code, answer.TxResponse.RawLog)
	}
	return s.landed(ctx, msg, answer.TxResponse.TxHash)
}

// landed waits for the block that runs the message. The chain answers a broadcast the moment it takes
// the transaction, so without this the next one would sign with a sequence the account no longer has,
// and a message the chain then refused would read as done
func (s *Signer) landed(ctx context.Context, msg sdk.Msg, hash string) error {
	for {
		answer, err := s.sender.GetTx(ctx, &txtypes.GetTxRequest{Hash: hash})
		switch {
		case err == nil && answer.TxResponse.Code != 0:
			return refused(msg, answer.TxResponse.Code, answer.TxResponse.RawLog)
		case err == nil:
			return nil
		}
		select {
		case <-ctx.Done():
			return shared.New("CHAIN_SLOW", shared.ErrUnavailable,
				fmt.Sprintf("the chain took %s as %s and never ran it", sdk.MsgTypeURL(msg), hash))
		case <-time.After(s.poll):
		}
	}
}

func refused(msg sdk.Msg, code uint32, log string) error {
	return shared.New("CHAIN_REFUSED", shared.ErrUnavailable,
		fmt.Sprintf("the chain refused %s with code %d: %s", sdk.MsgTypeURL(msg), code, log))
}

func (s *Signer) account(ctx context.Context) (number, sequence uint64, err error) {
	answer, err := s.accounts.Account(ctx, &authtypes.QueryAccountRequest{Address: string(s.key.Address())})
	if err != nil {
		return 0, 0, shared.New("ACCOUNT_UNKNOWN", shared.ErrNotFound,
			fmt.Sprintf("the chain holds no account for %s: it needs funds before it can sign", s.key.Address()))
	}
	var held authtypes.BaseAccount
	if err := held.Unmarshal(answer.Account.Value); err != nil {
		return 0, 0, err
	}
	return held.AccountNumber, held.Sequence, nil
}

// price is what the chain charges per unit of gas; a chain that does not say charges nothing
func (s *Signer) price(ctx context.Context) int64 {
	answer, err := s.query.Params(ctx, &types.QueryParamsRequest{})
	if err != nil || answer.Params.FeeParams == nil {
		return 0
	}
	return int64(answer.Params.FeeParams.MinGasPriceNgonka)
}
