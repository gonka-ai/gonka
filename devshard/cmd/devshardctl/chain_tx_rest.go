package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"devshard/signing"
)

const (
	defaultTxFeeDenom      = "ngonka"
	defaultTxFeeAmount     = uint64(1_000_000)
	defaultTxGasLimit      = uint64(500_000)
	defaultTxPollInterval  = 2 * time.Second
	defaultTxPollTimeout   = 45 * time.Second
	defaultUnorderedTxTTL  = 9 * time.Minute
	createEscrowMsgTypeURL = "/inference.inference.MsgCreateDevshardEscrow"
	settleEscrowMsgTypeURL = "/inference.inference.MsgSettleDevshardEscrow"
	secp256k1PubKeyTypeURL = "/cosmos.crypto.secp256k1.PubKey"
)

type RESTChainTxClient struct {
	baseURL      string
	txQueryURL   string
	chainID      string
	feeDenom     string
	feeAmount    uint64
	gasLimit     uint64
	pollInterval time.Duration
	pollTimeout  time.Duration
	client       *http.Client
}

type RESTChainTxConfig struct {
	BaseURL      string
	TxQueryURL   string
	ChainID      string
	FeeDenom     string
	FeeAmount    uint64
	GasLimit     uint64
	PollInterval time.Duration
	PollTimeout  time.Duration
	HTTPClient   *http.Client
}

type CreateDevshardEscrowResult struct {
	EscrowID uint64 `json:"escrow_id"`
	TxHash   string `json:"tx_hash"`
	Creator  string `json:"creator"`
}

type SettleDevshardEscrowResult struct {
	EscrowID uint64 `json:"escrow_id"`
	TxHash   string `json:"tx_hash"`
	Settler  string `json:"settler"`
}

type chainAccount struct {
	AccountNumber uint64
	Sequence      uint64
}

func NewRESTChainTxClient(cfg RESTChainTxConfig) (*RESTChainTxClient, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("chain REST URL is required")
	}
	feeDenom := strings.TrimSpace(cfg.FeeDenom)
	if feeDenom == "" {
		feeDenom = defaultTxFeeDenom
	}
	feeAmount := cfg.FeeAmount
	if feeAmount == 0 {
		feeAmount = defaultTxFeeAmount
	}
	gasLimit := cfg.GasLimit
	if gasLimit == 0 {
		gasLimit = defaultTxGasLimit
	}
	pollInterval := cfg.PollInterval
	if pollInterval <= 0 {
		pollInterval = defaultTxPollInterval
	}
	pollTimeout := cfg.PollTimeout
	if pollTimeout <= 0 {
		pollTimeout = defaultTxPollTimeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &RESTChainTxClient{
		baseURL:      baseURL,
		txQueryURL:   strings.TrimRight(strings.TrimSpace(cfg.TxQueryURL), "/"),
		chainID:      strings.TrimSpace(cfg.ChainID),
		feeDenom:     feeDenom,
		feeAmount:    feeAmount,
		gasLimit:     gasLimit,
		pollInterval: pollInterval,
		pollTimeout:  pollTimeout,
		client:       client,
	}, nil
}

func (c *RESTChainTxClient) CreateDevshardEscrow(ctx context.Context, signer *signing.Secp256k1Signer, amount uint64, modelID string) (*CreateDevshardEscrowResult, error) {
	if c == nil {
		return nil, fmt.Errorf("chain tx client is nil")
	}
	if signer == nil {
		return nil, fmt.Errorf("signer is required")
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return nil, fmt.Errorf("model_id is required")
	}
	if amount == 0 {
		return nil, fmt.Errorf("amount is required")
	}
	creator := signer.Address()
	chainID := c.chainID
	if chainID == "" {
		var err error
		chainID, err = c.fetchChainID(ctx)
		if err != nil {
			return nil, err
		}
	}
	account, err := c.fetchAccount(ctx, creator)
	if err != nil {
		return nil, err
	}

	txBytes, err := buildCreateDevshardEscrowTx(signer, chainID, account, c.feeDenom, c.feeAmount, c.gasLimit, amount, modelID)
	if err != nil {
		return nil, err
	}
	txHash, err := c.broadcastTx(ctx, txBytes)
	if err != nil {
		return nil, err
	}
	escrowID, err := c.waitForCreatedEscrowID(ctx, txHash)
	if err != nil {
		return nil, err
	}
	return &CreateDevshardEscrowResult{
		EscrowID: escrowID,
		TxHash:   txHash,
		Creator:  creator,
	}, nil
}

func (c *RESTChainTxClient) SettleDevshardEscrow(ctx context.Context, signer *signing.Secp256k1Signer, settlement SettlementJSON) (*SettleDevshardEscrowResult, error) {
	if c == nil {
		return nil, fmt.Errorf("chain tx client is nil")
	}
	if signer == nil {
		return nil, fmt.Errorf("signer is required")
	}
	escrowID, err := strconv.ParseUint(strings.TrimSpace(settlement.EscrowID), 10, 64)
	if err != nil || escrowID == 0 {
		return nil, fmt.Errorf("invalid escrow_id %q", settlement.EscrowID)
	}
	settler := signer.Address()
	chainID := c.chainID
	if chainID == "" {
		var err error
		chainID, err = c.fetchChainID(ctx)
		if err != nil {
			return nil, err
		}
	}
	account, err := c.fetchAccount(ctx, settler)
	if err != nil {
		return nil, err
	}
	txBytes, err := buildSettleDevshardEscrowTx(signer, chainID, account, c.feeDenom, c.feeAmount, c.gasLimit, settlement)
	if err != nil {
		return nil, err
	}
	txHash, err := c.broadcastTx(ctx, txBytes)
	if err != nil {
		return nil, err
	}
	return &SettleDevshardEscrowResult{
		EscrowID: escrowID,
		TxHash:   txHash,
		Settler:  settler,
	}, nil
}

func (c *RESTChainTxClient) fetchChainID(ctx context.Context) (string, error) {
	var payload any
	if err := c.getJSON(ctx, "/cosmos/base/tendermint/v1beta1/node_info", &payload); err != nil {
		return "", fmt.Errorf("fetch chain id: %w", err)
	}
	chainID := findStringField(payload, "network")
	if chainID == "" {
		return "", fmt.Errorf("chain id not found in node_info response")
	}
	return chainID, nil
}

func (c *RESTChainTxClient) fetchAccount(ctx context.Context, address string) (chainAccount, error) {
	var payload any
	if err := c.getJSON(ctx, "/cosmos/auth/v1beta1/accounts/"+url.PathEscape(address), &payload); err != nil {
		return chainAccount{}, fmt.Errorf("fetch account %s: %w", address, err)
	}
	accountNumber, ok := findUintField(payload, "account_number")
	if !ok {
		return chainAccount{}, fmt.Errorf("account_number not found for %s", address)
	}
	sequence, ok := findUintField(payload, "sequence")
	if !ok {
		return chainAccount{}, fmt.Errorf("sequence not found for %s", address)
	}
	return chainAccount{AccountNumber: accountNumber, Sequence: sequence}, nil
}

func (c *RESTChainTxClient) broadcastTx(ctx context.Context, txBytes []byte) (string, error) {
	reqBody := map[string]string{
		"tx_bytes": base64.StdEncoding.EncodeToString(txBytes),
		"mode":     "BROADCAST_MODE_SYNC",
	}
	var payload txResponseEnvelope
	if err := c.postJSON(ctx, "/cosmos/tx/v1beta1/txs", reqBody, &payload); err != nil {
		return "", fmt.Errorf("broadcast tx: %w", err)
	}
	if payload.TxResponse.Code != 0 {
		return "", fmt.Errorf("broadcast tx failed code=%d codespace=%s raw_log=%s", payload.TxResponse.Code, payload.TxResponse.Codespace, payload.TxResponse.RawLog)
	}
	txHash := strings.TrimSpace(payload.TxResponse.TxHash)
	if txHash == "" {
		return "", fmt.Errorf("broadcast response missing txhash")
	}
	return txHash, nil
}

func (c *RESTChainTxClient) waitForCreatedEscrowID(ctx context.Context, txHash string) (uint64, error) {
	deadline := time.Now().Add(c.pollTimeout)
	var lastErr error
	queryURLs := c.txQueryBaseURLs()
	for {
		for _, baseURL := range queryURLs {
			var payload txResponseEnvelope
			err := c.getJSONFromBaseURL(ctx, baseURL, "/cosmos/tx/v1beta1/txs/"+url.PathEscape(txHash), &payload)
			if err == nil {
				if payload.TxResponse.Code != 0 {
					return 0, fmt.Errorf("tx %s failed code=%d codespace=%s raw_log=%s", txHash, payload.TxResponse.Code, payload.TxResponse.Codespace, payload.TxResponse.RawLog)
				}
				if escrowID, ok := payload.TxResponse.createdEscrowID(); ok {
					return escrowID, nil
				}
				lastErr = fmt.Errorf("tx %s committed via %s but escrow_id event was not found", txHash, baseURL)
			} else {
				lastErr = fmt.Errorf("%s: %w", baseURL, err)
			}
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return 0, fmt.Errorf("wait for tx %s: %w", txHash, lastErr)
			}
			return 0, fmt.Errorf("wait for tx %s timed out", txHash)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(c.pollInterval):
		}
	}
}

func (c *RESTChainTxClient) txQueryBaseURLs() []string {
	if c.txQueryURL == "" || c.txQueryURL == c.baseURL {
		return []string{c.baseURL}
	}
	return []string{c.baseURL, c.txQueryURL}
}

func (c *RESTChainTxClient) waitForTxSuccess(ctx context.Context, txHash string) error {
	deadline := time.Now().Add(c.pollTimeout)
	var lastErr error
	for {
		var payload txResponseEnvelope
		err := c.getJSON(ctx, "/cosmos/tx/v1beta1/txs/"+url.PathEscape(txHash), &payload)
		if err == nil {
			if payload.TxResponse.Code != 0 {
				return fmt.Errorf("tx %s failed code=%d codespace=%s raw_log=%s", txHash, payload.TxResponse.Code, payload.TxResponse.Codespace, payload.TxResponse.RawLog)
			}
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return fmt.Errorf("wait for tx %s: %w", txHash, lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.pollInterval):
		}
	}
}

func (c *RESTChainTxClient) getJSON(ctx context.Context, path string, out any) error {
	return c.getJSONFromBaseURL(ctx, c.baseURL, path, out)
}

func (c *RESTChainTxClient) getJSONFromBaseURL(ctx context.Context, baseURL, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("GET %s status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *RESTChainTxClient) postJSON(ctx context.Context, path string, in any, out any) error {
	data, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("POST %s status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type txResponseEnvelope struct {
	TxResponse txResponse `json:"tx_response"`
}

type txResponse struct {
	Code      uint32      `json:"code"`
	Codespace string      `json:"codespace"`
	TxHash    string      `json:"txhash"`
	RawLog    string      `json:"raw_log"`
	Events    []txEvent   `json:"events"`
	Logs      []txLogItem `json:"logs"`
}

type txLogItem struct {
	Events []txEvent `json:"events"`
}

type txEvent struct {
	Type       string        `json:"type"`
	Attributes []txAttribute `json:"attributes"`
}

type txAttribute struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (r txResponse) createdEscrowID() (uint64, bool) {
	if id, ok := createdEscrowIDFromEvents(r.Events); ok {
		return id, true
	}
	for _, logItem := range r.Logs {
		if id, ok := createdEscrowIDFromEvents(logItem.Events); ok {
			return id, true
		}
	}
	return 0, false
}

func createdEscrowIDFromEvents(events []txEvent) (uint64, bool) {
	for _, event := range events {
		if event.Type != "devshard_escrow_created" {
			continue
		}
		for _, attr := range event.Attributes {
			if attr.Key != "escrow_id" {
				continue
			}
			id, err := strconv.ParseUint(attr.Value, 10, 64)
			if err == nil && id > 0 {
				return id, true
			}
		}
	}
	return 0, false
}

func buildCreateDevshardEscrowTx(signer *signing.Secp256k1Signer, chainID string, account chainAccount, feeDenom string, feeAmount, gasLimit, amount uint64, modelID string) ([]byte, error) {
	if strings.TrimSpace(chainID) == "" {
		return nil, fmt.Errorf("chain id is required")
	}
	msg := encodeMsgCreateDevshardEscrow(signer.Address(), amount, modelID)
	bodyBytes := encodeUnorderedTxBody(encodeAny(createEscrowMsgTypeURL, msg), time.Now().UTC().Add(defaultUnorderedTxTTL))
	pubKey := encodeAny(secp256k1PubKeyTypeURL, encodeSecp256k1PubKey(signer.CompressedPublicKeyBytes()))
	authInfoBytes := encodeAuthInfo(pubKey, 0, feeDenom, feeAmount, gasLimit)
	signDoc := encodeSignDoc(bodyBytes, authInfoBytes, chainID, account.AccountNumber)
	sig, err := signer.Sign(signDoc)
	if err != nil {
		return nil, err
	}
	if len(sig) < 64 {
		return nil, fmt.Errorf("invalid signature length %d", len(sig))
	}
	return encodeTxRaw(bodyBytes, authInfoBytes, sig[:64]), nil
}

func buildSettleDevshardEscrowTx(signer *signing.Secp256k1Signer, chainID string, account chainAccount, feeDenom string, feeAmount, gasLimit uint64, settlement SettlementJSON) ([]byte, error) {
	if strings.TrimSpace(chainID) == "" {
		return nil, fmt.Errorf("chain id is required")
	}
	msg, err := encodeMsgSettleDevshardEscrow(signer.Address(), settlement)
	if err != nil {
		return nil, err
	}
	bodyBytes := encodeUnorderedTxBody(encodeAny(settleEscrowMsgTypeURL, msg), time.Now().UTC().Add(defaultUnorderedTxTTL))
	pubKey := encodeAny(secp256k1PubKeyTypeURL, encodeSecp256k1PubKey(signer.CompressedPublicKeyBytes()))
	authInfoBytes := encodeAuthInfo(pubKey, 0, feeDenom, feeAmount, gasLimit)
	signDoc := encodeSignDoc(bodyBytes, authInfoBytes, chainID, account.AccountNumber)
	sig, err := signer.Sign(signDoc)
	if err != nil {
		return nil, err
	}
	if len(sig) < 64 {
		return nil, fmt.Errorf("invalid signature length %d", len(sig))
	}
	return encodeTxRaw(bodyBytes, authInfoBytes, sig[:64]), nil
}

func findStringField(v any, key string) string {
	switch x := v.(type) {
	case map[string]any:
		if raw, ok := x[key]; ok {
			if s, ok := raw.(string); ok {
				return s
			}
		}
		for _, child := range x {
			if found := findStringField(child, key); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range x {
			if found := findStringField(child, key); found != "" {
				return found
			}
		}
	}
	return ""
}

func findUintField(v any, key string) (uint64, bool) {
	switch x := v.(type) {
	case map[string]any:
		if raw, ok := x[key]; ok {
			if parsed, ok := parseJSONUint(raw); ok {
				return parsed, true
			}
		}
		for _, child := range x {
			if found, ok := findUintField(child, key); ok {
				return found, true
			}
		}
	case []any:
		for _, child := range x {
			if found, ok := findUintField(child, key); ok {
				return found, true
			}
		}
	}
	return 0, false
}

func parseJSONUint(v any) (uint64, bool) {
	switch x := v.(type) {
	case string:
		parsed, err := strconv.ParseUint(x, 10, 64)
		return parsed, err == nil
	case float64:
		if x < 0 || x != float64(uint64(x)) {
			return 0, false
		}
		return uint64(x), true
	case json.Number:
		parsed, err := strconv.ParseUint(string(x), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func encodeMsgCreateDevshardEscrow(creator string, amount uint64, modelID string) []byte {
	var out []byte
	out = appendBytesField(out, 1, []byte(creator))
	out = appendVarintField(out, 2, amount)
	out = appendBytesField(out, 3, []byte(modelID))
	return out
}

func encodeMsgSettleDevshardEscrow(settler string, settlement SettlementJSON) ([]byte, error) {
	escrowID, err := strconv.ParseUint(strings.TrimSpace(settlement.EscrowID), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse escrow_id: %w", err)
	}
	stateRoot, err := base64.StdEncoding.DecodeString(settlement.StateRoot)
	if err != nil {
		return nil, fmt.Errorf("decode state_root: %w", err)
	}
	restHash, err := base64.StdEncoding.DecodeString(settlement.RestHash)
	if err != nil {
		return nil, fmt.Errorf("decode rest_hash: %w", err)
	}
	var out []byte
	out = appendBytesField(out, 1, []byte(settler))
	out = appendVarintField(out, 2, escrowID)
	out = appendBytesField(out, 3, stateRoot)
	out = appendVarintField(out, 4, settlement.Nonce)
	out = appendBytesField(out, 5, restHash)
	for _, hs := range settlement.HostStats {
		out = appendBytesField(out, 6, encodeSettlementHostStats(hs))
	}
	for _, sig := range settlement.Signatures {
		encoded, err := encodeSlotSignature(sig)
		if err != nil {
			return nil, err
		}
		out = appendBytesField(out, 7, encoded)
	}
	out = appendVarintField(out, 8, settlement.Fees)
	out = appendBytesField(out, 9, []byte(settlement.StateRootAndProtocolVersion))
	return out, nil
}

func encodeSettlementHostStats(hs HostStatsJSON) []byte {
	var out []byte
	out = appendVarintField(out, 1, uint64(hs.SlotID))
	out = appendVarintField(out, 2, uint64(hs.Missed))
	out = appendVarintField(out, 3, uint64(hs.Invalid))
	out = appendVarintField(out, 4, hs.Cost)
	out = appendVarintField(out, 5, uint64(hs.RequiredValidations))
	out = appendVarintField(out, 6, uint64(hs.CompletedValidations))
	return out
}

func encodeSlotSignature(sig SlotSignatureJSON) ([]byte, error) {
	sigBytes, err := base64.StdEncoding.DecodeString(sig.Signature)
	if err != nil {
		return nil, fmt.Errorf("decode signature for slot %d: %w", sig.SlotID, err)
	}
	var out []byte
	out = appendVarintField(out, 1, uint64(sig.SlotID))
	out = appendBytesField(out, 2, sigBytes)
	return out, nil
}

func encodeSecp256k1PubKey(key []byte) []byte {
	var out []byte
	out = appendBytesField(out, 1, key)
	return out
}

func encodeAny(typeURL string, value []byte) []byte {
	var out []byte
	out = appendBytesField(out, 1, []byte(typeURL))
	out = appendBytesField(out, 2, value)
	return out
}

func encodeTxBody(msgAny []byte) []byte {
	var out []byte
	out = appendBytesField(out, 1, msgAny)
	return out
}

func encodeUnorderedTxBody(msgAny []byte, timeout time.Time) []byte {
	var out []byte
	out = appendBytesField(out, 1, msgAny)
	out = appendVarintField(out, 4, 1)
	out = appendBytesField(out, 5, encodeTimestamp(timeout))
	return out
}

func encodeTimestamp(ts time.Time) []byte {
	var out []byte
	out = appendVarintField(out, 1, uint64(ts.Unix()))
	if nanos := ts.Nanosecond(); nanos != 0 {
		out = appendVarintField(out, 2, uint64(nanos))
	}
	return out
}

func encodeAuthInfo(pubKeyAny []byte, sequence uint64, feeDenom string, feeAmount uint64, gasLimit uint64) []byte {
	signerInfo := encodeSignerInfo(pubKeyAny, sequence)
	fee := encodeFee(feeDenom, feeAmount, gasLimit)
	var out []byte
	out = appendBytesField(out, 1, signerInfo)
	out = appendBytesField(out, 2, fee)
	return out
}

func encodeSignerInfo(pubKeyAny []byte, sequence uint64) []byte {
	var single []byte
	single = appendVarintField(single, 1, 1) // SIGN_MODE_DIRECT
	var modeInfo []byte
	modeInfo = appendBytesField(modeInfo, 1, single)
	var out []byte
	out = appendBytesField(out, 1, pubKeyAny)
	out = appendBytesField(out, 2, modeInfo)
	out = appendVarintField(out, 3, sequence)
	return out
}

func encodeFee(denom string, amount uint64, gasLimit uint64) []byte {
	var coin []byte
	coin = appendBytesField(coin, 1, []byte(denom))
	coin = appendBytesField(coin, 2, []byte(strconv.FormatUint(amount, 10)))
	var out []byte
	out = appendBytesField(out, 1, coin)
	out = appendVarintField(out, 2, gasLimit)
	return out
}

func encodeSignDoc(bodyBytes []byte, authInfoBytes []byte, chainID string, accountNumber uint64) []byte {
	var out []byte
	out = appendBytesField(out, 1, bodyBytes)
	out = appendBytesField(out, 2, authInfoBytes)
	out = appendBytesField(out, 3, []byte(chainID))
	out = appendVarintField(out, 4, accountNumber)
	return out
}

func encodeTxRaw(bodyBytes []byte, authInfoBytes []byte, signature []byte) []byte {
	var out []byte
	out = appendBytesField(out, 1, bodyBytes)
	out = appendBytesField(out, 2, authInfoBytes)
	out = appendBytesField(out, 3, signature)
	return out
}

func appendVarintField(dst []byte, fieldNumber int, value uint64) []byte {
	dst = appendVarint(dst, uint64(fieldNumber<<3))
	return appendVarint(dst, value)
}

func appendBytesField(dst []byte, fieldNumber int, value []byte) []byte {
	dst = appendVarint(dst, uint64(fieldNumber<<3|2))
	dst = appendVarint(dst, uint64(len(value)))
	return append(dst, value...)
}

func appendVarint(dst []byte, value uint64) []byte {
	for value >= 0x80 {
		dst = append(dst, byte(value)|0x80)
		value >>= 7
	}
	return append(dst, byte(value))
}

func txSettingDurationMS(raw string, fallback time.Duration) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ms <= 0 {
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
}

func normalizePrivateKeyHex(key string) string {
	return strings.TrimPrefix(strings.TrimSpace(key), "0x")
}

func signerFromRequestKey(privateKey, privateKeyEnv string) (*signing.Secp256k1Signer, string, error) {
	keyHex := normalizePrivateKeyHex(privateKey)
	envName := strings.TrimSpace(privateKeyEnv)
	if keyHex == "" && envName != "" {
		keyHex = normalizePrivateKeyHex(os.Getenv(envName))
	}
	if keyHex == "" {
		return nil, "", errors.New("private_key or private_key_env is required")
	}
	signer, err := signing.SignerFromHex(keyHex)
	if err != nil {
		return nil, "", err
	}
	return signer, keyHex, nil
}
