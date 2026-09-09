package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	clienttx "github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"crypto/tls"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	bech32Prefix    = "gonka"
	defaultChainID  = "gonka-mainnet"
	gasLimit        = uint64(200_000)
	gasPrice        = "0ngonka"
)

var sdkConfigOnce sync.Once

func init() {
	sdkConfigOnce.Do(func() {
		cfg := sdk.GetConfig()
		cfg.SetBech32PrefixForAccount(bech32Prefix, bech32Prefix+"pub")
		cfg.Seal()
	})
}

func deriveAddress(privKeyHex string) (string, error) {
	b, err := hex.DecodeString(privKeyHex)
	if err != nil {
		return "", fmt.Errorf("decode private key hex: %w", err)
	}
	privKey := &secp256k1.PrivKey{Key: b}
	addr := sdk.AccAddress(privKey.PubKey().Address())
	return addr.String(), nil
}

func newRegistry() codectypes.InterfaceRegistry {
	reg := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(reg)
	authtypes.RegisterInterfaces(reg)
	inferencetypes.RegisterInterfaces(reg)
	return reg
}

func createEscrow(ctx context.Context, grpcAddr, chainID, privKeyHex string, amount uint64, modelID string, grpcTLS bool) (uint64, error) {
	b, err := hex.DecodeString(privKeyHex)
	if err != nil {
		return 0, fmt.Errorf("decode key: %w", err)
	}
	privKey := &secp256k1.PrivKey{Key: b}
	address := sdk.AccAddress(privKey.PubKey().Address()).String()

	registry := newRegistry()
	cdc := codec.NewProtoCodec(registry)

	const keyName = "signer"
	kr := keyring.NewInMemory(cdc)
	if err := kr.ImportPrivKeyHex(keyName, privKeyHex, "secp256k1"); err != nil {
		return 0, fmt.Errorf("import key: %w", err)
	}

	var dialCreds grpc.DialOption
	if grpcTLS {
		dialCreds = grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{InsecureSkipVerify: true})) //nolint:gosec // SSH tunnel: cert hostname won't match localhost
	} else {
		dialCreds = grpc.WithTransportCredentials(insecure.NewCredentials())
	}
	conn, err := grpc.NewClient(grpcAddr, dialCreds)
	if err != nil {
		return 0, fmt.Errorf("grpc connect: %w", err)
	}
	defer conn.Close()

	txConfig := authtx.NewTxConfig(cdc, []signingtypes.SignMode{signingtypes.SignMode_SIGN_MODE_DIRECT})

	accNum, seq, err := accountInfo(ctx, conn, registry, address)
	if err != nil {
		return 0, fmt.Errorf("account info: %w", err)
	}

	msg := &inferencetypes.MsgCreateDevshardEscrow{
		Creator: address,
		Amount:  amount,
		ModelId: modelID,
	}

	factory := clienttx.Factory{}.
		WithKeybase(kr).
		WithTxConfig(txConfig).
		WithChainID(chainID).
		WithAccountNumber(accNum).
		WithSequence(seq).
		WithGas(gasLimit).
		WithGasPrices(gasPrice).
		WithFromName(keyName)

	txBuilder, err := factory.BuildUnsignedTx(msg)
	if err != nil {
		return 0, fmt.Errorf("build tx: %w", err)
	}
	if err := clienttx.Sign(ctx, factory, keyName, txBuilder, true); err != nil {
		return 0, fmt.Errorf("sign tx: %w", err)
	}

	raw, err := txConfig.TxEncoder()(txBuilder.GetTx())
	if err != nil {
		return 0, fmt.Errorf("encode tx: %w", err)
	}

	svc := txtypes.NewServiceClient(conn)
	resp, err := svc.BroadcastTx(ctx, &txtypes.BroadcastTxRequest{
		TxBytes: raw,
		Mode:    txtypes.BroadcastMode_BROADCAST_MODE_SYNC,
	})
	if err != nil {
		return 0, fmt.Errorf("broadcast: %w", err)
	}
	if resp.TxResponse.Code != 0 {
		return 0, fmt.Errorf("tx failed code=%d log=%s", resp.TxResponse.Code, resp.TxResponse.RawLog)
	}

	return waitForEscrowID(ctx, svc, resp.TxResponse.TxHash)
}

func accountInfo(ctx context.Context, conn grpc.ClientConnInterface, registry codectypes.InterfaceRegistry, address string) (accNum, seq uint64, err error) {
	qc := authtypes.NewQueryClient(conn)
	res, err := qc.Account(ctx, &authtypes.QueryAccountRequest{Address: address})
	if err != nil {
		return 0, 0, err
	}
	var acc sdk.AccountI
	if err := registry.UnpackAny(res.Account, &acc); err != nil {
		return 0, 0, fmt.Errorf("unpack account: %w", err)
	}
	return acc.GetAccountNumber(), acc.GetSequence(), nil
}

type escrowInfo struct {
	Slots      []string `json:"slots"`
	EpochIndex uint64   `json:"epoch_index,string"`
}

func fetchEscrowInfo(restURL string, escrowID uint64) *escrowInfo {
	url := fmt.Sprintf("%s/productscience/inference/inference/devshard_escrow/%d", restURL, escrowID)
	resp, err := http.Get(url) //nolint:gosec
	if err != nil {
		return nil
	}
	var body struct {
		Escrow escrowInfo `json:"escrow"`
	}
	err = json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if err != nil {
		return nil
	}
	return &body.Escrow
}

func fetchEscrowSlots(restURL string, escrowID uint64) []string {
	info := fetchEscrowInfo(restURL, escrowID)
	if info == nil {
		return nil
	}
	return info.Slots
}

func fetchEscrowEpoch(restURL string, escrowID uint64) uint64 {
	info := fetchEscrowInfo(restURL, escrowID)
	if info == nil {
		return 0
	}
	return info.EpochIndex
}

// fetchHostEpochStats queries per-host validation stats for a given epoch.
func fetchHostEpochStats(restURL string, epochIndex uint64, participant string) {
	url := fmt.Sprintf("%s/productscience/inference/inference/devshard_host_epoch_stats/%d/%s", restURL, epochIndex, participant)
	resp, err := http.Get(url) //nolint:gosec
	if err != nil {
		fmt.Printf("  stats query failed for %s: %v\n", participant, err)
		return
	}
	defer resp.Body.Close()

	var body struct {
		Stats struct {
			Participant          string `json:"participant"`
			EpochIndex           uint64 `json:"epoch_index,string"`
			RequiredValidations  uint32 `json:"required_validations"`
			CompletedValidations uint32 `json:"completed_validations"`
			Missed               uint32 `json:"missed"`
			Invalid              uint32 `json:"invalid"`
			Cost                 uint64 `json:"cost,string"`
			EscrowCount          uint32 `json:"escrow_count"`
		} `json:"stats"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		fmt.Printf("  stats decode failed for %s: %v\n", participant, err)
		return
	}
	s := body.Stats
	fmt.Printf("  %s epoch=%d escrows=%d required=%d completed=%d missed=%d invalid=%d cost=%d\n",
		s.Participant, s.EpochIndex, s.EscrowCount, s.RequiredValidations, s.CompletedValidations, s.Missed, s.Invalid, s.Cost)
}

func filterOpenEscrows(restURL string, ids []uint64) []uint64 {
	var open []uint64
	for _, id := range ids {
		url := fmt.Sprintf("%s/productscience/inference/inference/devshard_escrow/%d", restURL, id)
		resp, err := http.Get(url) //nolint:gosec
		if err != nil {
			fmt.Printf("  escrow %d: REST check failed (%v), skipping\n", id, err)
			continue
		}
		var body struct {
			Found  bool `json:"found"`
			Escrow struct {
				Settled bool `json:"settled"`
			} `json:"escrow"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil || !body.Found || body.Escrow.Settled {
			fmt.Printf("  escrow %d: not open (found=%v settled=%v), skipping\n", id, body.Found, body.Escrow.Settled)
			continue
		}
		open = append(open, id)
	}
	return open
}

func waitForEscrowID(ctx context.Context, svc txtypes.ServiceClient, txHash string) (uint64, error) {
	for i := 0; i < 30; i++ {
		res, err := svc.GetTx(ctx, &txtypes.GetTxRequest{Hash: txHash})
		if err == nil && res.TxResponse != nil && res.TxResponse.Code == 0 {
			for _, event := range res.TxResponse.Events {
				if event.Type == "devshard_escrow_created" {
					for _, attr := range event.Attributes {
						if attr.Key == "escrow_id" {
							id, err := strconv.ParseUint(attr.Value, 10, 64)
							if err != nil {
								return 0, fmt.Errorf("parse escrow_id %q: %w", attr.Value, err)
							}
							return id, nil
						}
					}
				}
			}
		}
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return 0, fmt.Errorf("timeout waiting for tx %s inclusion", txHash)
}
