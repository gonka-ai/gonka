// Command aps-client is a reference client for the optional signed agent
// request envelope. It generates a principal key and an agent key, builds and
// signs an envelope, and prints the X-Agent-Passport and X-Agent-Sig headers
// plus a ready-to-run curl command.
//
// In production the principal signature is produced by the developer's real
// Gonka account key, for example through Keplr or the chain CLI. This demo
// generates a throwaway principal key so the example is self-contained. The
// envelope is additive metadata: a real request still carries Gonka's existing
// developer signature and requester headers.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"decentralized-api/internal/agentenvelope"
	"decentralized-api/internal/agentenvelope/jcs"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

const (
	chainID = "gonka-mainnet"
	model   = "Qwen/QwQ-32B"
	path    = "/v1/chat/completions"
	apiURL  = "http://localhost:9000"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	// Step 1: keys. The principal key anchors to a Gonka account. The agent
	// key is the portable per-request signing key.
	principalPriv := secp256k1.GenPrivKey()
	principalPub := principalPriv.PubKey().(*secp256k1.PubKey)
	principalAddr := sdk.AccAddress(principalPub.Address()).String()

	agentPub, agentPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}

	// Step 2: the envelope. The principal delegates a scoped capability to
	// the agent: one model, a 30 minute lifetime, one beneficiary.
	now := time.Now().UTC()
	models := []string{model}
	env := agentenvelope.EnvelopeV1{
		Version:          agentenvelope.SchemaVersion,
		Audience:         agentenvelope.Audience,
		ChainID:          chainID,
		AgentID:          "urn:aps:agent:demo",
		AgentPubkey:      agentenvelope.TypedKey{Type: agentenvelope.KeyTypeEd25519, PublicKey: base64.StdEncoding.EncodeToString(agentPub)},
		PrincipalAddress: principalAddr,
		PrincipalPubkey:  agentenvelope.TypedKey{Type: agentenvelope.KeyTypeSecp256k1, PublicKey: base64.StdEncoding.EncodeToString(principalPub.Key)},
		Scope: agentenvelope.Scope{
			AllowedModels: &models,
			ExpiresAt:     now.Add(30 * time.Minute),
		},
		Beneficiary: "urn:customer:demo-corp",
		IssuedAt:    now,
	}

	// Step 3: the principal signature (ADR-036). In production this is done
	// by the developer's real key, for example via Keplr signArbitrary.
	env.PrincipalSig = ""
	unsigned, err := json.Marshal(env)
	if err != nil {
		return err
	}
	signBytes, err := agentenvelope.PrincipalSignBytes(principalAddr, unsigned)
	if err != nil {
		return err
	}
	psig, err := principalPriv.Sign(signBytes)
	if err != nil {
		return err
	}
	env.PrincipalSig = base64.StdEncoding.EncodeToString(psig)

	rawEnvelope, err := json.Marshal(env)
	if err != nil {
		return err
	}

	// Step 4: the per-request body and the agent signature over it.
	body := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"Hello"}]}`)
	envJCS, err := jcs.Canonicalize(rawEnvelope)
	if err != nil {
		return err
	}
	payload := agentenvelope.AgentSigPayload(chainID, http.MethodPost, path, envJCS, body)
	agentSig := ed25519.Sign(agentPriv, payload)

	passportHeader := base64.StdEncoding.EncodeToString(rawEnvelope)
	agentSigHeader := base64.StdEncoding.EncodeToString(agentSig)

	fmt.Println("Signed agent request envelope")
	fmt.Println("  principal:", principalAddr)
	fmt.Println("  agent_id: ", env.AgentID)
	fmt.Println("  model:    ", model)
	fmt.Println()
	fmt.Println("X-Agent-Passport:", passportHeader)
	fmt.Println()
	fmt.Println("X-Agent-Sig:", agentSigHeader)
	fmt.Println()
	fmt.Println("Example request (the envelope headers are additive to Gonka's existing request auth):")
	fmt.Printf("curl -sS %s%s \\\n", apiURL, path)
	fmt.Printf("  -H 'Content-Type: application/json' \\\n")
	fmt.Printf("  -H 'X-Agent-Passport: %s' \\\n", passportHeader)
	fmt.Printf("  -H 'X-Agent-Sig: %s' \\\n", agentSigHeader)
	fmt.Printf("  --data '%s'\n", string(body))
	return nil
}
