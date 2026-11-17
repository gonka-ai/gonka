package chain

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	commonchain "common/chain"
	cmtservice "github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// NoMaxModelLen is the --max-model-len of a model whose args carry no plain number for it.
const (
	NoMaxModelLen uint64 = 0

	maxModelLenFlag = "--max-model-len"
	txNotFoundText  = "tx not found"
)

// Reader is the chain state the observer polls, an interface so tests answer it without a connection.
type Reader interface {
	EscrowParams(ctx context.Context) (EscrowParams, bool, error)
	PreservedNodes(ctx context.Context) (*PreservedNodes, bool, error)
	Models(ctx context.Context) (map[string]ModelParams, error)
}

// Transport is the chain access a transaction needs: the reads a signed body carries, the broadcast, and the query that says whether it executed.
type Transport interface {
	ChainID(ctx context.Context) (string, error)
	Account(ctx context.Context, address string) (Account, error)
	SpendableBalance(ctx context.Context, address, denom string) (uint64, error)
	Broadcast(ctx context.Context, txBytes []byte) (string, error)
	Tx(ctx context.Context, txHash string) (TxResult, bool, error)
	Escrow(ctx context.Context, escrowID uint64) (EscrowInfo, bool, error)
}

// Account is the pair a signed transaction needs; an unordered tx signs the sequence as zero, so only the number reaches the wire.
type Account struct {
	Number   uint64
	Sequence uint64
}

// TxResult is a committed transaction as the gateway reads it: the code that says whether it executed, and the events an escrow id is recovered from.
type TxResult struct {
	Code      uint32
	Codespace string
	RawLog    string
	Events    []TxEvent
}

type TxEvent struct {
	Type       string
	Attributes []TxAttribute
}

type TxAttribute struct {
	Key   string
	Value string
}

// EscrowParams is what the chain stamps on every escrow it creates. See README.md, "What the chain observer provides".
type EscrowParams struct {
	MaxNonce          uint64
	TokenPrice        uint64
	FeePerNonce       uint64
	CreateDevshardFee uint64
}

// PreservedNodes is the PoC preserved set, keyed the way routing reads it.
type PreservedNodes struct {
	EpisodeAnchorHeight int64
	Models              []PreservedModel
}

type PreservedModel struct {
	ModelID      string
	Participants []PreservedParticipant
}

type PreservedParticipant struct {
	ParticipantID string
	NodeIDs       []string
}

// GRPCChain answers both interfaces over the one connection the gateway dials. See README.md, "The gRPC transport".
type GRPCChain struct {
	client   *commonchain.Client
	registry codectypes.InterfaceRegistry
	chainID  string
}

func NewGRPCChain(client *commonchain.Client, chainID string) *GRPCChain {
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	authtypes.RegisterInterfaces(registry)
	return &GRPCChain{client: client, registry: registry, chainID: strings.TrimSpace(chainID)}
}

func (g *GRPCChain) conn() grpc.ClientConnInterface { return g.client.Conn() }

// ChainID asks the node unless a value was configured; a mismatch invalidates every signature, so the configured value wins.
func (g *GRPCChain) ChainID(ctx context.Context) (string, error) {
	if g.chainID != "" {
		return g.chainID, nil
	}
	nodeInfo, err := g.client.CometServiceClient().GetNodeInfo(ctx, &cmtservice.GetNodeInfoRequest{})
	if err != nil {
		return "", fmt.Errorf("fetch chain id: %w", err)
	}
	network := strings.TrimSpace(nodeInfo.GetDefaultNodeInfo().GetNetwork())
	if network == "" {
		return "", fmt.Errorf("chain id not reported by node")
	}
	return network, nil
}

// Account reads through the account's own registered type, so a vesting or module account answers correctly.
func (g *GRPCChain) Account(ctx context.Context, address string) (Account, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return Account{}, fmt.Errorf("address is required")
	}
	response, err := authtypes.NewQueryClient(g.conn()).Account(ctx, &authtypes.QueryAccountRequest{Address: address})
	if err != nil {
		return Account{}, fmt.Errorf("fetch account %s: %w", address, err)
	}
	var account sdk.AccountI
	if err := g.registry.UnpackAny(response.GetAccount(), &account); err != nil {
		return Account{}, fmt.Errorf("unpack account %s: %w", address, err)
	}
	return Account{Number: account.GetAccountNumber(), Sequence: account.GetSequence()}, nil
}

// SpendableBalance leaves out vesting coins still locked, which a create cannot spend.
func (g *GRPCChain) SpendableBalance(ctx context.Context, address, denom string) (uint64, error) {
	response, err := banktypes.NewQueryClient(g.conn()).SpendableBalanceByDenom(ctx, &banktypes.QuerySpendableBalanceByDenomRequest{Address: address, Denom: denom})
	if err != nil {
		return 0, fmt.Errorf("fetch spendable %s of %s: %w", denom, address, err)
	}
	balance := response.GetBalance()
	switch {
	case balance == nil || balance.Amount.IsNil():
		return 0, nil
	case !balance.Amount.IsUint64():
		return math.MaxUint64, nil
	}
	return balance.Amount.Uint64(), nil
}

func (g *GRPCChain) Broadcast(ctx context.Context, txBytes []byte) (string, error) {
	response, err := txtypes.NewServiceClient(g.conn()).BroadcastTx(ctx, &txtypes.BroadcastTxRequest{
		TxBytes: txBytes,
		Mode:    txtypes.BroadcastMode_BROADCAST_MODE_SYNC,
	})
	if err != nil {
		return "", fmt.Errorf("broadcast tx: %w", err)
	}
	return broadcastTxHash(response.GetTxResponse())
}

func broadcastTxHash(result *sdk.TxResponse) (string, error) {
	if result == nil {
		return "", fmt.Errorf("broadcast response carried no result")
	}
	if result.Code != 0 {
		failure := fmt.Errorf("broadcast tx failed code=%d codespace=%s raw_log=%s",
			result.Code, result.Codespace, result.RawLog)
		if alreadyInMempool(result) {
			return "", failure
		}
		return "", fmt.Errorf("%w: %w", ErrTxRejected, failure)
	}
	txHash := strings.TrimSpace(result.TxHash)
	if txHash == "" {
		return "", fmt.Errorf("broadcast response missing txhash")
	}
	return txHash, nil
}

func alreadyInMempool(result *sdk.TxResponse) bool {
	return result.Codespace == sdkerrors.ErrTxInMempoolCache.Codespace() && result.Code == sdkerrors.ErrTxInMempoolCache.ABCICode()
}

// Tx reports found=false for a transaction the node has not indexed yet, which a poller must tell apart from a failure.
func (g *GRPCChain) Tx(ctx context.Context, txHash string) (TxResult, bool, error) {
	response, err := txtypes.NewServiceClient(g.conn()).GetTx(ctx, &txtypes.GetTxRequest{Hash: txHash})
	if err != nil {
		if isNotFoundStatus(err) {
			return TxResult{}, false, nil
		}
		return TxResult{}, false, fmt.Errorf("fetch tx %s: %w", txHash, err)
	}
	result := response.GetTxResponse()
	if result == nil {
		return TxResult{}, false, nil
	}
	converted := TxResult{Code: result.Code, Codespace: result.Codespace, RawLog: result.RawLog}
	for _, event := range result.Events {
		attributes := make([]TxAttribute, 0, len(event.Attributes))
		for _, attribute := range event.Attributes {
			attributes = append(attributes, TxAttribute{Key: attribute.Key, Value: attribute.Value})
		}
		converted.Events = append(converted.Events, TxEvent{Type: event.Type, Attributes: attributes})
	}
	return converted, true, nil
}

// Escrow reports found=false only when the chain says the escrow is absent; a transport failure is an error.
func (g *GRPCChain) Escrow(ctx context.Context, escrowID uint64) (EscrowInfo, bool, error) {
	response, err := g.client.InferenceQueryClient().DevshardEscrow(ctx, &inferencetypes.QueryGetDevshardEscrowRequest{Id: escrowID})
	if err != nil {
		return EscrowInfo{}, false, fmt.Errorf("fetch escrow %d: %w", escrowID, err)
	}
	if !response.GetFound() || response.GetEscrow() == nil {
		return EscrowInfo{}, false, nil
	}
	return EscrowInfo{
		EscrowID:   fmt.Sprintf("%d", escrowID),
		Balance:    response.GetEscrow().GetAmount(),
		EpochIndex: response.GetEscrow().GetEpochIndex(),
		Settled:    response.GetEscrow().GetSettled(),
	}, true, nil
}

// Models reads what governance says about every model; an empty map means the chain named none.
func (g *GRPCChain) Models(ctx context.Context) (map[string]ModelParams, error) {
	response, err := g.client.InferenceQueryClient().ModelsAll(ctx, &inferencetypes.QueryModelsAllRequest{})
	if err != nil {
		return nil, fmt.Errorf("fetch governance models: %w", err)
	}
	models := make(map[string]ModelParams, len(response.GetModel()))
	for _, model := range response.GetModel() {
		models[model.GetId()] = ModelParams{ContextWindow: model.GetContextWindow(), MaxModelLen: maxModelLenOf(model.GetModelArgs())}
	}
	return models, nil
}

// maxModelLenOf reads vLLM's --max-model-len from governance model_args; NoMaxModelLen when the args carry no plain number for it.
func maxModelLenOf(args []string) uint64 {
	for index, arg := range args {
		value, joined := strings.CutPrefix(arg, maxModelLenFlag+"=")
		switch {
		case joined:
		case arg == maxModelLenFlag && index+1 < len(args):
			value = args[index+1]
		default:
			continue
		}
		parsed, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return NoMaxModelLen
		}
		return parsed
	}
	return NoMaxModelLen
}

// EscrowParams reports fetched=false when the chain carries no devshard escrow params -- not enabled, rather than unread.
func (g *GRPCChain) EscrowParams(ctx context.Context) (EscrowParams, bool, error) {
	response, err := g.client.InferenceQueryClient().Params(ctx, &inferencetypes.QueryParamsRequest{})
	if err != nil {
		return EscrowParams{}, false, fmt.Errorf("fetch devshard escrow params: %w", err)
	}
	params := response.GetParams().DevshardEscrowParams
	if params == nil {
		return EscrowParams{}, false, nil
	}
	return EscrowParams{MaxNonce: uint64(params.GetMaxNonce()), TokenPrice: params.GetTokenPrice(), FeePerNonce: params.GetFeePerNonce(), CreateDevshardFee: params.GetCreateDevshardFee()}, true, nil
}

// PreservedNodes reports found=false when the chain holds no snapshot for the current episode, which routing reads as "no preserved set".
func (g *GRPCChain) PreservedNodes(ctx context.Context) (*PreservedNodes, bool, error) {
	response, err := g.client.InferenceQueryClient().PreservedNodesSnapshot(ctx, &inferencetypes.QueryPreservedNodesSnapshotRequest{})
	if err != nil {
		return nil, false, fmt.Errorf("fetch preserved nodes snapshot: %w", err)
	}
	snapshot := response.GetSnapshot()
	if !response.GetFound() || snapshot == nil {
		return nil, false, nil
	}
	converted := &PreservedNodes{EpisodeAnchorHeight: snapshot.GetEpisodeAnchorHeight()}
	for _, modelNodes := range snapshot.GetModelPreservedNodes() {
		model := PreservedModel{ModelID: modelNodes.GetModelId()}
		for _, participant := range modelNodes.GetParticipants() {
			model.Participants = append(model.Participants, PreservedParticipant{
				ParticipantID: participant.GetParticipantId(),
				NodeIDs:       append([]string(nil), participant.GetNodeIds()...),
			})
		}
		converted.Models = append(converted.Models, model)
	}
	return converted, true, nil
}

// isNotFoundStatus reads the typed code, and the SDK's own text only from a node that answers Unknown: a proxy's 404 or any other failure keeps the intent it was checking.
func isNotFoundStatus(err error) bool {
	switch status.Code(err) {
	case codes.NotFound:
		return true
	case codes.Unknown:
		return strings.Contains(strings.ToLower(status.Convert(err).Message()), txNotFoundText)
	}
	return false
}

// EpochGroupData answers an epoch's model snapshot, which the stop-token vocabulary reads a model's pinned tokenizer from.
func (g *GRPCChain) EpochGroupData(ctx context.Context, request *inferencetypes.QueryGetEpochGroupDataRequest, options ...grpc.CallOption) (*inferencetypes.QueryGetEpochGroupDataResponse, error) {
	return g.client.InferenceQueryClient().EpochGroupData(ctx, request, options...)
}
