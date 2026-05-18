package simulation

import (
	"encoding/base64"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/testutil/simsx"

	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

// Sim-only signing helpers for Phase 2 first-wave factories.
//
// The on-chain signature scheme used by `verifyStartFirstMessageKeys`
// (msg_server_start_inference.go) and `verifyFinishKeys`
// (msg_server_finish_inference.go) is:
//
//   bytes = Payload || strconv(EpochId if>0) || strconv(Timestamp) ||
//           TransferAddress (|| ExecutorAddress for TA/Executor)
//   sig   = base64( secp256k1_sign(privKey, bytes) )  // r||s, 64 bytes
//
// Encoding lives in calculations/signature_validate.go. Factories
// in this package generate the components from msg fields and sign with
// the picked SimAccount's PrivKey so the produced tx clears the cryptographic
// verify path and the inference actually lands in the keeper.

// simAccountSigner adapts a simtypes.Account PrivKey to the
// calculations.Signer interface (SignBytes returns base64-encoded sig).
type simAccountSigner struct{ priv cryptotypes.PrivKey }

func (s simAccountSigner) SignBytes(data []byte) (string, error) {
	sig, err := s.priv.Sign(data)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// SignDevStart signs MsgStartInference dev components. Result goes in
// msg.InferenceId (the field is named after the inference but is treated
// by the handler as the dev signature — see
// msg_server_start_inference.go calculations.SignatureData
// `DevSignature: msg.InferenceId`).
func SignDevStart(sa simsx.SimAccount, msg *types.MsgStartInference) (string, error) {
	components := calculations.SignatureComponents{
		Payload:         msg.OriginalPromptHash,
		Timestamp:       msg.RequestTimestamp,
		TransferAddress: msg.Creator,
	}
	return calculations.Sign(simAccountSigner{priv: sa.PrivKey}, components, calculations.Developer)
}

// SignDevFinish signs MsgFinishInference dev components. Result goes in
// msg.InferenceId. Mirrors getFinishDevSignatureComponents at
// msg_server_finish_inference.go.
func SignDevFinish(sa simsx.SimAccount, msg *types.MsgFinishInference) (string, error) {
	components := calculations.SignatureComponents{
		Payload:         msg.OriginalPromptHash,
		Timestamp:       msg.RequestTimestamp,
		TransferAddress: msg.TransferredBy,
	}
	return calculations.Sign(simAccountSigner{priv: sa.PrivKey}, components, calculations.Developer)
}

// SignTAFinish signs MsgFinishInference TA components. Result goes in
// msg.TransferSignature. Mirrors getFinishTASignatureComponents at
// msg_server_finish_inference.go (TA/Executor share bytes layout).
func SignTAFinish(sa simsx.SimAccount, msg *types.MsgFinishInference) (string, error) {
	components := calculations.SignatureComponents{
		Payload:         msg.PromptHash,
		Timestamp:       msg.RequestTimestamp,
		TransferAddress: msg.TransferredBy,
		ExecutorAddress: msg.ExecutedBy,
	}
	return calculations.Sign(simAccountSigner{priv: sa.PrivKey}, components, calculations.TransferAgent)
}
