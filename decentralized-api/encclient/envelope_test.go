package encclient

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
)

func TestConstantsMatchPython(t *testing.T) {
	cases := []struct {
		name string
		got  any
		want any
	}{
		{"EnvelopeVersion", EnvelopeVersion, 1},
		{"KEMID", KEMID, uint16(0x0020)},
		{"KDFID", KDFID, uint16(0x0001)},
		{"AEADID", AEADID, uint16(0x0003)},
		{"ExportKeyLen", ExportKeyLen, 32},
		{"ResponseNonceLen", ResponseNonceLen, 12},
		{"HPKEInfo", string(HPKEInfo), "gonka/devshard/hpke-info/v1"},
		{"aadLabelRequest", string(aadLabelRequest), "gonka/devshard/req/v1"},
		{"aadLabelResponse", string(aadLabelResponse), "gonka/devshard/resp/v1"},
		{"exportLabelResponse", string(exportLabelResponse), "gonka/devshard/response-key/v1"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestBuildAADLayout(t *testing.T) {
	sessionID := "session-abc"
	var nonce uint64 = 0x0102030405060708
	ph := bytes.Repeat([]byte{0xAA}, 32)
	nonceBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(nonceBytes, nonce)

	wantReq := bytes.Join([][]byte{aadLabelRequest, []byte(sessionID), nonceBytes, ph}, []byte{0x00})
	if got := buildRequestAAD(sessionID, nonce, ph); !bytes.Equal(got, wantReq) {
		t.Fatalf("request AAD layout mismatch:\n got = %x\nwant = %x", got, wantReq)
	}

	wantResp := bytes.Join([][]byte{aadLabelResponse, []byte(sessionID), nonceBytes}, []byte{0x00})
	if got := buildResponseAAD(sessionID, nonce); !bytes.Equal(got, wantResp) {
		t.Fatalf("response AAD layout mismatch:\n got = %x\nwant = %x", got, wantResp)
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	pub, priv, err := generateKeyPair()
	if err != nil {
		t.Fatalf("generateKeyPair: %v", err)
	}

	sessionID := "session-roundtrip"
	var nonce uint64 = 42
	payload := []byte(`{"model":"llama","messages":[{"role":"user","content":"hi"}]}`)

	env, senderRespKey, err := SealRequest(pub, sessionID, nonce, payload)
	if err != nil {
		t.Fatalf("SealRequest: %v", err)
	}
	if env.Version != EnvelopeVersion || env.KEMID != KEMID || env.KDFID != KDFID || env.AEADID != AEADID {
		t.Fatalf("envelope suite ids drifted: %+v", env)
	}
	if env.SessionID != sessionID || env.Nonce != nonce {
		t.Fatalf("envelope head mismatch: %+v", env)
	}
	if len(senderRespKey) != ExportKeyLen {
		t.Fatalf("response key length = %d, want %d", len(senderRespKey), ExportKeyLen)
	}

	plaintext, recipientRespKey, err := openRequestForTest(priv, env)
	if err != nil {
		t.Fatalf("openRequestForTest: %v", err)
	}
	if !bytes.Equal(plaintext, payload) {
		t.Fatalf("plaintext mismatch: got %q want %q", plaintext, payload)
	}
	if !bytes.Equal(senderRespKey, recipientRespKey) {
		t.Fatal("sender and recipient response keys differ")
	}

	respPayload := []byte(`{"id":"resp-1","choices":[{"message":{"content":"hello"}}]}`)
	resp, err := sealResponseForTest(recipientRespKey, sessionID, nonce, respPayload)
	if err != nil {
		t.Fatalf("sealResponseForTest: %v", err)
	}
	opened, err := OpenResponse(senderRespKey, resp)
	if err != nil {
		t.Fatalf("OpenResponse: %v", err)
	}
	if !bytes.Equal(opened, respPayload) {
		t.Fatalf("response plaintext mismatch: got %q want %q", opened, respPayload)
	}
}

func TestOpenRequestRejectsTamperedAAD(t *testing.T) {
	pub, priv, _ := generateKeyPair()
	env, _, err := SealRequest(pub, "real-session", 1, []byte("hi"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	env.SessionID = "evil-session"
	if _, _, err := openRequestForTest(priv, env); err == nil {
		t.Fatal("expected open to fail after AAD tamper")
	}
}

func TestOpenResponseRejectsAEADMismatch(t *testing.T) {
	resp := &EncryptedResponse{
		Version:       EnvelopeVersion,
		AEADID:        0x0001,
		ResponseNonce: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0}, ResponseNonceLen)),
	}
	_, err := OpenResponse(bytes.Repeat([]byte{0}, ExportKeyLen), resp)
	if err == nil || !strings.Contains(err.Error(), "AEAD") {
		t.Fatalf("expected AEAD mismatch error, got %v", err)
	}
}

func TestEnvelopeJSONShape(t *testing.T) {
	pub, _, _ := generateKeyPair()
	env, _, err := SealRequest(pub, "s", 1, []byte("x"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{
		`"version":`, `"session_id":`, `"nonce":`, `"recipient_key_id":`,
		`"kem_id":`, `"kdf_id":`, `"aead_id":`,
		`"enc":`, `"prompt_hash":`, `"ciphertext":`,
	} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("envelope JSON missing field %s: %s", key, raw)
		}
	}
}

func TestSealRequestRejectsBadPublicKeyLength(t *testing.T) {
	if _, _, err := SealRequest(make([]byte, 16), "s", 0, []byte("x")); err == nil {
		t.Fatal("expected error for short public key")
	}
}

func TestRecipientKeyIDDeterministicHexLen(t *testing.T) {
	pub, _, err := generateKeyPair()
	if err != nil {
		t.Fatalf("generateKeyPair: %v", err)
	}
	id1 := RecipientKeyID(pub)
	id2 := RecipientKeyID(pub)
	if id1 != id2 {
		t.Fatalf("RecipientKeyID is not deterministic: %s vs %s", id1, id2)
	}
	if len(id1) != RecipientKeyIDHexLen {
		t.Fatalf("RecipientKeyID length = %d, want %d", len(id1), RecipientKeyIDHexLen)
	}
	if RecipientKeyID(nil) != "" {
		t.Fatal("RecipientKeyID(nil) must be empty")
	}
	pub2, _, _ := generateKeyPair()
	if id1 == RecipientKeyID(pub2) {
		t.Fatal("distinct keys must produce distinct key_ids")
	}
}

func TestSealRequestPopulatesRecipientKeyID(t *testing.T) {
	pub, _, err := generateKeyPair()
	if err != nil {
		t.Fatalf("generateKeyPair: %v", err)
	}
	env, _, err := SealRequest(pub, "s", 1, []byte("x"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if env.RecipientKeyID != RecipientKeyID(pub) {
		t.Fatalf("envelope recipient_key_id = %q, want %q", env.RecipientKeyID, RecipientKeyID(pub))
	}
}
