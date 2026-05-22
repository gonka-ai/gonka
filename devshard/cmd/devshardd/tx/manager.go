package tx

import (
	"context"
	"fmt"

	"github.com/cosmos/cosmos-sdk/client"
	clienttx "github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc"
)

// Manager signs and broadcasts MsgSettleDevshardEscrow.
// No batching, no retry — caller handles retry if needed.
type Manager struct {
	client   grpc.ClientConnInterface
	keyring  keyring.Keyring
	txConfig client.TxConfig
	registry codectypes.InterfaceRegistry
	signer   string // key name in keyring
	address  string // bech32 address
	chainID  string
}

// New creates a Manager. kr must already contain the key named by cfg.SignerKeyName.
// address is the bech32 address corresponding to that key.
func New(conn grpc.ClientConnInterface, kr keyring.Keyring, address, signerKeyName, chainID string) (*Manager, error) {
	if chainID == "" {
		return nil, fmt.Errorf("chain id is required")
	}

	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	authtypes.RegisterInterfaces(registry)
	inferencetypes.RegisterInterfaces(registry)

	cdc := codec.NewProtoCodec(registry)
	txConfig := authtx.NewTxConfig(cdc, []signingtypes.SignMode{signingtypes.SignMode_SIGN_MODE_DIRECT})
	// TODO: get chainId from chain itself?
	return &Manager{
		client:   conn,
		keyring:  kr,
		txConfig: txConfig,
		registry: registry,
		signer:   signerKeyName,
		address:  address,
		chainID:  chainID,
	}, nil
}

// SubmitDisputeState signs and broadcasts MsgSettleDevshardEscrow with the
// host's computed state root and collected slot signatures.
func (m *Manager) SubmitDisputeState(escrowID uint64, stateRoot []byte, nonce uint64, sigs map[uint32][]byte) error {
	signatures := make([]*inferencetypes.DevshardSlotSignature, 0, len(sigs))
	for slotID, sig := range sigs {
		signatures = append(signatures, &inferencetypes.DevshardSlotSignature{
			SlotId:    slotID,
			Signature: sig,
		})
	}

	ctx := context.Background()
	accNum, seq, err := m.accountInfo(ctx)
	if err != nil {
		return fmt.Errorf("tx: get account info: %w", err)
	}

	msg := &inferencetypes.MsgSettleDevshardEscrow{
		Settler:    m.address,
		EscrowId:   escrowID,
		StateRoot:  stateRoot,
		Nonce:      nonce,
		Signatures: signatures,
	}

	factory := clienttx.Factory{}.
		WithKeybase(m.keyring).
		WithTxConfig(m.txConfig).
		WithChainID(m.chainID).
		WithAccountNumber(accNum).
		WithSequence(seq).
		WithGas(200_000).
		WithGasPrices("0ngonka").
		WithFromName(m.signer)

	txBuilder, err := factory.BuildUnsignedTx(msg)
	if err != nil {
		return fmt.Errorf("tx: build MsgSettleDevshardEscrow (dispute): %w", err)
	}
	if err := clienttx.Sign(ctx, factory, m.signer, txBuilder, true); err != nil {
		return fmt.Errorf("tx: sign MsgSettleDevshardEscrow (dispute): %w", err)
	}

	raw, err := m.txConfig.TxEncoder()(txBuilder.GetTx())
	if err != nil {
		return fmt.Errorf("tx: encode MsgSettleDevshardEscrow (dispute): %w", err)
	}

	svc := txtypes.NewServiceClient(m.client)
	resp, err := svc.BroadcastTx(ctx, &txtypes.BroadcastTxRequest{
		TxBytes: raw,
		Mode:    txtypes.BroadcastMode_BROADCAST_MODE_SYNC,
	})
	if err != nil {
		return fmt.Errorf("tx: broadcast MsgSettleDevshardEscrow (dispute): %w", err)
	}
	if resp.TxResponse.Code != 0 {
		return fmt.Errorf("tx: MsgSettleDevshardEscrow (dispute) failed code=%d log=%s", resp.TxResponse.Code, resp.TxResponse.RawLog)
	}
	return nil
}

// accountInfo fetches the account number and sequence for m.address.
func (m *Manager) accountInfo(ctx context.Context) (accNum, seq uint64, err error) {
	addr, err := sdk.AccAddressFromBech32(m.address)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid address %q: %w", m.address, err)
	}

	qc := authtypes.NewQueryClient(m.client)
	res, err := qc.Account(ctx, &authtypes.QueryAccountRequest{Address: addr.String()})
	if err != nil {
		return 0, 0, err
	}

	var acc sdk.AccountI
	if err := m.registry.UnpackAny(res.Account, &acc); err != nil {
		return 0, 0, fmt.Errorf("unpack account: %w", err)
	}
	return acc.GetAccountNumber(), acc.GetSequence(), nil
}
