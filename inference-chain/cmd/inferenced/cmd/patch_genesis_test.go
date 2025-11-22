package cmd

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	signv2 "github.com/cosmos/cosmos-sdk/types/tx/signing"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

func TestAddParticipantToGenesis_AddAndUpsert(t *testing.T) {
	// Initial minimal inference module genesis state
	inferenceGenesis := map[string]any{
		"params":              map[string]any{},
		"genesis_only_params": map[string]any{},
		"model_list":          []any{},
		"bridge":              map[string]any{},
		"participant_list":    []any{},
	}
	infBz, _ := json.Marshal(inferenceGenesis)

	app := make(map[string]json.RawMessage)
	app["inference"] = infBz

	// 1) Add new participant
	msg := &inferencetypes.MsgSubmitNewParticipant{
		Creator:      "gonka1alice0000000000000000000000000000000",
		Url:          "https://alice.example",
		ValidatorKey: "BASE64VALPUBKEY",
		WorkerKey:    "BASE64WORKERKEY",
	}

	if err := addParticipantToGenesis(nil, app, msg); err != nil {
		t.Fatalf("addParticipantToGenesis failed: %v", err)
	}

	// Validate state: contains exactly one participant matching msg
	var infOut struct {
		ParticipantList []inferencetypes.Participant `json:"participant_list"`
	}
	if err := json.Unmarshal(app["inference"], &infOut); err != nil {
		t.Fatalf("failed to decode updated inference state: %v", err)
	}
	if len(infOut.ParticipantList) != 1 {
		t.Fatalf("expected 1 participant, got %d", len(infOut.ParticipantList))
	}
	p := infOut.ParticipantList[0]
	if p.Index != msg.Creator || p.Address != msg.Creator {
		t.Fatalf("participant Index/Address mismatch: %s %s", p.Index, p.Address)
	}
	if p.InferenceUrl != msg.Url {
		t.Fatalf("participant Url mismatch: %s", p.InferenceUrl)
	}
	if p.ValidatorKey != msg.ValidatorKey || p.WorkerPublicKey != msg.WorkerKey {
		t.Fatalf("participant keys mismatch")
	}

	// 2) Upsert same Index with different URL, expect replacement not duplication
	msg2 := &inferencetypes.MsgSubmitNewParticipant{
		Creator:      msg.Creator,
		Url:          "https://alice.new",
		ValidatorKey: msg.ValidatorKey,
		WorkerKey:    msg.WorkerKey,
	}
	if err := addParticipantToGenesis(nil, app, msg2); err != nil {
		t.Fatalf("addParticipantToGenesis upsert failed: %v", err)
	}
	if err := json.Unmarshal(app["inference"], &infOut); err != nil {
		t.Fatalf("failed to decode updated inference state (upsert): %v", err)
	}
	if len(infOut.ParticipantList) != 1 {
		t.Fatalf("expected 1 participant after upsert, got %d", len(infOut.ParticipantList))
	}
	if infOut.ParticipantList[0].InferenceUrl != msg2.Url {
		t.Fatalf("expected url to be replaced to %s, got %s", msg2.Url, infOut.ParticipantList[0].InferenceUrl)
	}
}

func TestAddAuthzGrantToGenesis_AddInitAndUpsert(t *testing.T) {
	// Start with empty AppState (authz missing) to test initialization path
	app := make(map[string]json.RawMessage)

	// Prepare a MsgGrant with a GenericAuthorization
	typeURL := "/inference.inference.MsgStartInference"
	expire := time.Now().Add(24 * time.Hour)
	genAuth := authztypes.NewGenericAuthorization(typeURL)
	// Create deterministic bytes for addresses (20 bytes required)
	granter := sdk.AccAddress([]byte("granter-address-32-granter-addr-0001")[:20])
	grantee := sdk.AccAddress([]byte("grantee-address-32-grantee-addr-0001")[:20])
	msg, err := authztypes.NewMsgGrant(granter, grantee, genAuth, &expire)
	if err != nil {
		t.Fatalf("failed to create msg grant: %v", err)
	}

	// 1) Add first grant – should create authz module section
	// Provide a codec so GenericAuthorization can be unpacked
	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	if err := addAuthzGrantToGenesis(cdc, app, msg); err != nil {
		t.Fatalf("addAuthzGrantToGenesis failed: %v", err)
	}

	var azOut struct {
		Authorization []authztypes.GrantAuthorization `json:"authorization"`
	}
	if err := json.Unmarshal(app["authz"], &azOut); err != nil {
		t.Fatalf("failed to decode authz genesis: %v", err)
	}
	if len(azOut.Authorization) != 1 {
		t.Fatalf("expected 1 grant, got %d", len(azOut.Authorization))
	}
	if azOut.Authorization[0].Granter != msg.Granter || azOut.Authorization[0].Grantee != msg.Grantee {
		t.Fatalf("granter/grantee mismatch")
	}
	// JSON-grant based path does not store direct Any, so we check via JSON structure instead
	// Expect @type GenericAuthorization and msg == typeURL when codec is provided
	var azJSON map[string]any
	if err := json.Unmarshal(app["authz"], &azJSON); err != nil {
		t.Fatalf("failed to decode authz json: %v", err)
	}
	authList, _ := azJSON["authorization"].([]any)
	if len(authList) != 1 {
		t.Fatalf("expected 1 grant in json, got %d", len(authList))
	}
	first, _ := authList[0].(map[string]any)
	auth, _ := first["authorization"].(map[string]any)
	if auth["@type"] != "/cosmos.authz.v1beta1.GenericAuthorization" {
		t.Fatalf("unexpected authorization json: %v", auth)
	}

	// 2) Upsert same (granter, grantee, type_url) – should replace not duplicate
	newExpire := time.Now().Add(48 * time.Hour)
	genAuth2 := authztypes.NewGenericAuthorization(typeURL)
	// Convert back from bech32 strings in msg to AccAddress for helper
	msg2, err := authztypes.NewMsgGrant(sdk.MustAccAddressFromBech32(msg.Granter), sdk.MustAccAddressFromBech32(msg.Grantee), genAuth2, &newExpire)
	if err != nil {
		t.Fatalf("failed to create msg grant2: %v", err)
	}
	if err := addAuthzGrantToGenesis(cdc, app, msg2); err != nil {
		t.Fatalf("addAuthzGrantToGenesis upsert failed: %v", err)
	}
	if err := json.Unmarshal(app["authz"], &azOut); err != nil {
		t.Fatalf("failed to decode authz genesis after upsert: %v", err)
	}
	if len(azOut.Authorization) != 1 {
		t.Fatalf("expected 1 grant after upsert, got %d", len(azOut.Authorization))
	}
	// Validate expiration using direct JSON to avoid Any/time codec nuances
	var azJSON2 map[string]any
	if err := json.Unmarshal(app["authz"], &azJSON2); err != nil {
		t.Fatalf("failed to decode authz json after upsert: %v", err)
	}
	authList2, _ := azJSON2["authorization"].([]any)
	first2, _ := authList2[0].(map[string]any)
	expStr2, _ := first2["expiration"].(string)
	if expStr2 != newExpire.UTC().Format(time.RFC3339) {
		t.Fatalf("expected expiration to be replaced")
	}

	// 3) Add different type_url – should append (now total 2)
	genAuth3 := authztypes.NewGenericAuthorization("/inference.inference.MsgFinishInference")
	msg3, err := authztypes.NewMsgGrant(sdk.MustAccAddressFromBech32(msg.Granter), sdk.MustAccAddressFromBech32(msg.Grantee), genAuth3, &newExpire)
	if err != nil {
		t.Fatalf("failed to create msg grant3: %v", err)
	}
	if err := addAuthzGrantToGenesis(cdc, app, msg3); err != nil {
		t.Fatalf("addAuthzGrantToGenesis second type failed: %v", err)
	}
	if err := json.Unmarshal(app["authz"], &azOut); err != nil {
		t.Fatalf("failed to decode authz genesis after append: %v", err)
	}
	if len(azOut.Authorization) != 2 {
		t.Fatalf("expected 2 grants after append, got %d", len(azOut.Authorization))
	}
}

func TestPatchGenesis_ApplyRealGenparticipantFile(t *testing.T) {
	// Initialize minimal app genesis state with empty inference participants and authz authorizations
	initInference := map[string]any{
		"params":              map[string]any{},
		"genesis_only_params": map[string]any{},
		"model_list":          []any{},
		"participant_list":    []any{},
		"bridge":              map[string]any{},
	}
	initAuthz := map[string]any{
		"authorization": []any{},
	}
	infBz, _ := json.Marshal(initInference)
	azBz, _ := json.Marshal(initAuthz)
	app := map[string]json.RawMessage{
		"inference": infBz,
		"authz":     azBz,
	}

	// Load the test genparticipant file from testdata
	path := "testdata/genparticipant-test.json"
	var txFile struct {
		Body struct {
			Messages []json.RawMessage `json:"messages"`
		} `json:"body"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("test data file not found: %v", err)
	}
	if err := json.Unmarshal(data, &txFile); err != nil {
		t.Fatalf("failed to unmarshal example tx: %v", err)
	}

	uniqueGrants := make(map[string]struct{})
	var seenCreator string

	for _, raw := range txFile.Body.Messages {
		var meta map[string]any
		if err := json.Unmarshal(raw, &meta); err != nil {
			t.Fatalf("failed to unmarshal message meta: %v", err)
		}
		tpe, _ := meta["@type"].(string)
		switch tpe {
		case "/inference.inference.MsgSubmitNewParticipant":
			// Map to SDK message type and apply
			creator := meta["creator"].(string)
			url := meta["url"].(string)
			vkey, _ := meta["validator_key"].(string)
			wkey, _ := meta["worker_key"].(string)
			m := &inferencetypes.MsgSubmitNewParticipant{Creator: creator, Url: url, ValidatorKey: vkey, WorkerKey: wkey}
			if err := addParticipantToGenesis(nil, app, m); err != nil {
				t.Fatalf("addParticipantToGenesis failed: %v", err)
			}
			seenCreator = creator
		case "/cosmos.authz.v1beta1.MsgGrant":
			granter := meta["granter"].(string)
			grantee := meta["grantee"].(string)
			grantObj := meta["grant"].(map[string]any)
			authObj := grantObj["authorization"].(map[string]any)
			innerMsg, _ := authObj["msg"].(string)
			expStr, _ := grantObj["expiration"].(string)
			var exp *time.Time
			if expStr != "" {
				if tt, err := time.Parse(time.RFC3339Nano, expStr); err == nil {
					exp = &tt
				}
			}
			genAuth := authztypes.NewGenericAuthorization(innerMsg)
			// Pack to Any
			anyAuth, err := codectypes.NewAnyWithValue(genAuth)
			if err != nil {
				// Fallback: manual Any using Marshal
				bz, merr := genAuth.Marshal()
				if merr != nil {
					t.Fatalf("failed to marshal GenericAuthorization: %v", merr)
				}
				anyAuth = &codectypes.Any{TypeUrl: "/cosmos.authz.v1beta1.GenericAuthorization", Value: bz}
			}
			msg := &authztypes.MsgGrant{Granter: granter, Grantee: grantee, Grant: authztypes.Grant{Authorization: anyAuth, Expiration: exp}}
			if err := addAuthzGrantToGenesis(nil, app, msg); err != nil {
				t.Fatalf("addAuthzGrantToGenesis failed: %v", err)
			}
			key := granter + "|" + grantee + "|" + innerMsg
			uniqueGrants[key] = struct{}{}
		}
	}

	// Validate inference participant was added
	var infOut struct {
		ParticipantList []inferencetypes.Participant `json:"participant_list"`
	}
	if err := json.Unmarshal(app["inference"], &infOut); err != nil {
		t.Fatalf("failed to unmarshal inference state: %v", err)
	}
	if len(infOut.ParticipantList) == 0 {
		t.Fatalf("participant_list is empty after applying example file")
	}
	if seenCreator != "" && infOut.ParticipantList[0].Index == "" {
		t.Fatalf("participant Index missing")
	}

	// Validate number of unique grants applied equals count from file
	var azOut struct {
		Authorization []map[string]any `json:"authorization"`
	}
	if err := json.Unmarshal(app["authz"], &azOut); err != nil {
		t.Fatalf("failed to unmarshal authz state: %v", err)
	}
	if len(azOut.Authorization) != len(uniqueGrants) {
		t.Fatalf("expected %d unique grants, got %d", len(uniqueGrants), len(azOut.Authorization))
	}
}

// TestVerifyTransactionSignatures_ValidAndInvalid builds a minimal Tx JSON with a real secp256k1 key
// and verifies that verifyTransactionSignatures accepts a correct signature and rejects an invalid one.
func TestVerifyTransactionSignatures_ValidAndInvalid(t *testing.T) {
	// Build interface registry and codec with crypto types registered
	interfaceRegistry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(interfaceRegistry)
	inferencetypes.RegisterInterfaces(interfaceRegistry)
	cdc := codec.NewProtoCodec(interfaceRegistry)

	// Build tx config
	txConfig := authtx.NewTxConfig(cdc, authtx.DefaultSignModes)

	// Prepare a client context stub with codec and tx config
	clientCtx := client.Context{}.
		WithCodec(cdc).
		WithInterfaceRegistry(interfaceRegistry).
		WithTxConfig(txConfig)

	// Generate a secp256k1 keypair
	priv := secp256k1.GenPrivKey()
	pub := priv.PubKey()

	// Build tx using TxBuilder
	builder := txConfig.NewTxBuilder()
	// Add a dummy message for testing
	dummyMsg := &inferencetypes.MsgSubmitNewParticipant{
		Creator:      "gonka1test000000000000000000000000000000000",
		Url:          "https://test.example",
		ValidatorKey: "testvalidatorkey",
		WorkerKey:    "testworkerkey",
	}
	builder.SetMsgs(dummyMsg)
	// Minimal fee
	builder.SetFeeAmount(sdk.NewCoins())
	builder.SetGasLimit(0)

	chainID := "test-chain-id"
	accountNumber := uint64(0)
	sequence := uint64(0)

	// Build signer data and sign doc bytes using manual path
	// Encode the current (unsigned) tx to derive the body/authinfo bytes
	// First set a placeholder signature to populate SignerInfos with mode and pubkey
	sigData := &signv2.SingleSignatureData{SignMode: signv2.SignMode_SIGN_MODE_DIRECT}
	placeholder := signv2.SignatureV2{PubKey: pub, Data: sigData, Sequence: sequence}
	if err := builder.SetSignatures(placeholder); err != nil {
		t.Fatalf("set placeholder sig: %v", err)
	}

	// Build sign bytes: SignDoc{BodyBytes, AuthInfoBytes, ChainID, AccountNumber}
	// Encode tx to bytes then unmarshal into proto Tx to extract body/authinfo
	txBytes, err := txConfig.TxEncoder()(builder.GetTx())
	if err != nil {
		t.Fatalf("encode placeholder tx: %v", err)
	}

	var txProto txtypes.Tx
	if err := cdc.Unmarshal(txBytes, &txProto); err != nil {
		t.Fatalf("unmarshal tx: %v", err)
	}

	bodyBytes, err := cdc.Marshal(txProto.Body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	authInfoBytes, err := cdc.Marshal(txProto.AuthInfo)
	if err != nil {
		t.Fatalf("marshal authinfo: %v", err)
	}
	signDoc := txtypes.SignDoc{BodyBytes: bodyBytes, AuthInfoBytes: authInfoBytes, ChainId: chainID, AccountNumber: accountNumber}
	signBytes, err := signDoc.Marshal()
	if err != nil {
		t.Fatalf("marshal signdoc: %v", err)
	}

	// Sign and set final signature
	sigBz, err := priv.Sign(signBytes)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	finalSig := signv2.SignatureV2{PubKey: pub, Data: &signv2.SingleSignatureData{SignMode: signv2.SignMode_SIGN_MODE_DIRECT, Signature: sigBz}, Sequence: sequence}
	if err := builder.SetSignatures(finalSig); err != nil {
		t.Fatalf("set final sig: %v", err)
	}

	sdkTx := builder.GetTx()

	// Positive case: signature verifies
	if err := verifyTransactionSignatures(clientCtx, chainID, sdkTx); err != nil {
		t.Fatalf("expected valid signature, got error: %v", err)
	}

	// Negative case: corrupt signature
	badSig := append([]byte{0x00}, sigBz...)
	badFinal := signv2.SignatureV2{PubKey: pub, Data: &signv2.SingleSignatureData{SignMode: signv2.SignMode_SIGN_MODE_DIRECT, Signature: badSig}, Sequence: sequence}
	if err := builder.SetSignatures(badFinal); err != nil {
		t.Fatalf("set bad sig: %v", err)
	}
	badTx := builder.GetTx()
	if err := verifyTransactionSignatures(clientCtx, chainID, badTx); err == nil {
		t.Fatalf("expected signature verification to fail for corrupted signature")
	}
}
