package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Configuration constants
const (
	NginxConfigPath    = "/etc/nginx/conf.d/whitelist_ips.conf"
	PollMinInterval    = 60 * time.Second
	PollMaxInterval    = 30 * time.Minute
	ErrorWaitTime      = 30 * time.Second // Retry fast on errors
	DefaultBlockTime   = 8 * time.Second
	SafetyBufferBlocks = 2 // number of blocks to buffer
	ApiTimeout         = 10 * time.Second
)

// Environment variables
var (
	ApiUrl     string // e.g. "http://localhost:9000"
	NodeRPCUrl string // e.g. "http://localhost:26657"
	KeyPrefix  string // e.g. "active_validators" for logging
)

// Constants for sidecar-managed nginx artifacts
const (
	BlacklistConfigPath = "/etc/nginx/conf.d/blacklist_ips.conf"
	LogFilePath         = "/var/log/nginx/access_json.log"
	RPCMethodLogSocket  = "/var/log/nginx/rpc_method_log.sock"
)

// Global Managers
var (
	GlobalReloadManager *ReloadManager
	GlobalBanManager    *BanManager
)

var rpcMethodLoggingEnabled bool

// --------------------------------------------------------------------------------
// BanManager (Fail2Ban Logic)
// --------------------------------------------------------------------------------

type BanManager struct {
	mu             sync.RWMutex
	scores         map[string]int       // IP -> Score
	bannedIPs      map[string]time.Time // IP -> ExpirationTime
	whitelist      map[string]bool      // Cache of currently whitelisted IPs
	banDuration    time.Duration
	maxRetries     int
	scoreWeights   map[int]int
	scoreLastSeen  map[string]time.Time
	scoreTTL       time.Duration
	flushChan      chan struct{}
	trustedProxies []*net.IPNet
}

func NewBanManager(duration time.Duration, retries int, weights map[int]int) *BanManager {
	bm := &BanManager{
		scores:        make(map[string]int),
		bannedIPs:     make(map[string]time.Time),
		whitelist:     make(map[string]bool),
		banDuration:   duration,
		maxRetries:    retries,
		scoreWeights:  weights,
		scoreLastSeen: make(map[string]time.Time),
		scoreTTL:      5 * time.Minute,
		flushChan:     make(chan struct{}, 1),
	}

	// Parse Trusted Real-IP Ranges
	realIPFrom := os.Getenv("PROXY_REAL_IP_FROM")
	if realIPFrom != "" {
		for _, cidr := range strings.Fields(realIPFrom) {
			_, netCIDR, err := net.ParseCIDR(cidr)
			if err == nil {
				bm.trustedProxies = append(bm.trustedProxies, netCIDR)
				continue
			}

			// Try single IP
			ip := net.ParseIP(cidr)
			if ip != nil {
				// Convert single IP to /32 or /128 CIDR
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				mask := net.CIDRMask(bits, bits)
				bm.trustedProxies = append(bm.trustedProxies, &net.IPNet{IP: ip, Mask: mask})
				continue
			}

			logBan("Warning: Invalid CIDR/IP in PROXY_REAL_IP_FROM: %s", cidr)
		}
	}

	go bm.startExpirer()
	go bm.flushWorker()
	return bm
}

// UpdateWhitelist is called by the WhitelistSyncer to keep BanManager aware of trusted IPs
// PRECEDENCE RULE: Whitelist > Blacklist.
func (bm *BanManager) UpdateWhitelist(ips []string) {
	bm.mu.Lock()

	newMap := make(map[string]bool)
	dirty := false
	for _, ip := range ips {
		newMap[ip] = true
		// Immediate unban if a trusted IP was accidentally banned
		if _, exists := bm.bannedIPs[ip]; exists {
			logBan("Removing whitelisted IP %s from ban list.", ip)
			delete(bm.bannedIPs, ip)
			dirty = true
		}
	}
	bm.whitelist = newMap
	bm.mu.Unlock()

	// Flush to disk if we removed any bans
	if dirty {
		logBan("Whitelist update cleared some bans. Flushing blacklist.")
		bm.requestFlush()
	}
}

// AccessLogLine matches the 'json_combined' format in Nginx
type AccessLogLine struct {
	RemoteAddr string `json:"remote_addr"`
	Status     int    `json:"status"`
	Request    string `json:"request"`
	TimeLocal  string `json:"time_local"`
}

func (bm *BanManager) ProcessLogLine(line []byte) {
	var entry AccessLogLine
	if err := json.Unmarshal(line, &entry); err != nil {
		// Ignore malformed lines (text logs mixed in?)
		return
	}

	bm.ProcessEntry(entry)
}

func extractChainRPCMethod(request string) (string, bool) {
	target, ok := requestTarget(request)
	if !ok {
		return "", false
	}

	u, err := url.ParseRequestURI(target)
	if err != nil {
		return "", false
	}

	const prefix = "/chain-rpc/"
	if !strings.HasPrefix(u.Path, prefix) {
		return "", false
	}

	method := strings.Trim(strings.TrimPrefix(u.Path, prefix), "/")
	if method == "" {
		return "-", true
	}

	if i := strings.IndexByte(method, '/'); i >= 0 {
		method = method[:i]
	}
	return sanitizeRPCMethod(method), true
}

func requestTarget(request string) (string, bool) {
	parts := strings.Fields(request)
	if len(parts) < 2 {
		return "", false
	}
	return parts[1], true
}

func sanitizeRPCMethod(method string) string {
	if method == "" {
		return "-"
	}

	var b strings.Builder
	for _, r := range method {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= 128 {
			break
		}
	}

	if b.Len() == 0 {
		return "-"
	}
	return b.String()
}

type jsonRPCRequest struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type rpcLogItem struct {
	Method string
	Params string
}

func startRPCMethodLogServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc-method-log", rpcMethodLogHandler)

	if err := os.RemoveAll(RPCMethodLogSocket); err != nil {
		logRPC("failed to remove stale RPC method log socket: %v", err)
		return
	}

	listener, err := net.Listen("unix", RPCMethodLogSocket)
	if err != nil {
		logRPC("failed to listen on RPC method log socket: %v", err)
		return
	}
	defer listener.Close()
	defer os.Remove(RPCMethodLogSocket)

	if err := os.Chmod(RPCMethodLogSocket, 0666); err != nil {
		logRPC("failed to chmod RPC method log socket: %v", err)
		return
	}

	logRPC("Starting RPC method log server on unix:%s", RPCMethodLogSocket)
	if err := http.Serve(listener, mux); err != nil {
		logRPC("RPC method log server stopped: %v", err)
	}
}

func rpcMethodLogHandler(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusNoContent)
	defer r.Body.Close()

	if !rpcMethodLoggingEnabled {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		logRPC("failed to read mirrored RPC request body: %v", err)
		return
	}

	items, batchSize, ok := extractJSONRPCLogItems(body)
	if !ok {
		if method, found := extractChainRPCMethod(r.Header.Get("X-Original-Request")); found {
			items = []rpcLogItem{{Method: method, Params: "-"}}
			batchSize = 0
			ok = true
		}
	}
	if !ok {
		return
	}

	logRPCMirrorLine(r, formatRPCMethods(items), formatRPCParams(items), batchSize)
}

func extractJSONRPCMethods(body []byte) ([]string, int, bool) {
	items, batchSize, ok := extractJSONRPCLogItems(body)
	if !ok {
		return nil, 0, false
	}

	methods := make([]string, 0, len(items))
	for _, item := range items {
		methods = append(methods, item.Method)
	}
	return methods, batchSize, true
}

func extractJSONRPCLogItems(body []byte) ([]rpcLogItem, int, bool) {
	body = []byte(strings.TrimSpace(string(body)))
	if len(body) == 0 {
		return nil, 0, false
	}

	if body[0] == '[' {
		var batch []jsonRPCRequest
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, 0, false
		}

		items := make([]rpcLogItem, 0, len(batch))
		for _, req := range batch {
			method := sanitizeRPCMethod(req.Method)
			items = append(items, rpcLogItem{
				Method: method,
				Params: summarizeRPCParams(method, req.Params),
			})
		}
		return items, len(batch), true
	}

	var req jsonRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, 0, false
	}
	method := sanitizeRPCMethod(req.Method)
	return []rpcLogItem{{
		Method: method,
		Params: summarizeRPCParams(method, req.Params),
	}}, 1, true
}

func formatRPCMethods(items []rpcLogItem) string {
	methods := make([]string, 0, len(items))
	for _, item := range items {
		methods = append(methods, item.Method)
	}
	return strings.Join(methods, ",")
}

func formatRPCParams(items []rpcLogItem) string {
	params := make([]string, 0, len(items))
	for _, item := range items {
		params = append(params, item.Params)
	}
	return strings.Join(params, ";")
}

func summarizeRPCParams(method string, raw json.RawMessage) string {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "-"
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err == nil && object != nil {
		return summarizeRPCParamObject(method, object)
	}

	var array []json.RawMessage
	if err := json.Unmarshal(raw, &array); err == nil {
		return summarizeRPCParamArray(method, array)
	}

	return "params_" + summarizeRawJSON(raw)
}

func summarizeRPCParamObject(method string, params map[string]json.RawMessage) string {
	switch method {
	case "abci_query":
		return joinParamParts(
			stringParam("path", params["path"]),
			summarizeOpaqueParam("data", params["data"]),
			scalarParam("height", params["height"]),
			scalarParam("prove", params["prove"]),
		)
	case "broadcast_tx_async", "broadcast_tx_sync", "broadcast_tx_commit":
		return joinParamParts(summarizeOpaqueParam("tx", params["tx"]))
	}

	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		raw := params[key]
		if isSensitiveRPCParam(key) {
			parts = append(parts, summarizeOpaqueParam(key, raw))
			continue
		}
		if value := scalarParam(key, raw); value != "" {
			parts = append(parts, value)
		} else {
			parts = append(parts, key+"_"+summarizeRawJSON(raw))
		}
	}
	return joinParamParts(parts...)
}

func summarizeRPCParamArray(method string, params []json.RawMessage) string {
	switch method {
	case "abci_query":
		return joinParamParts(
			positionalStringParam("path", params, 0),
			positionalOpaqueParam("data", params, 1),
			positionalScalarParam("height", params, 2),
			positionalScalarParam("prove", params, 3),
		)
	case "broadcast_tx_async", "broadcast_tx_sync", "broadcast_tx_commit":
		return joinParamParts(positionalOpaqueParam("tx", params, 0))
	}
	return fmt.Sprintf("array_len=%d", len(params))
}

func isSensitiveRPCParam(key string) bool {
	key = strings.ToLower(key)
	return strings.Contains(key, "tx") ||
		strings.Contains(key, "sig") ||
		strings.Contains(key, "signature") ||
		strings.Contains(key, "data") ||
		strings.Contains(key, "bytes") ||
		strings.Contains(key, "proof")
}

func stringParam(name string, raw json.RawMessage) string {
	value, ok := jsonString(raw)
	if !ok || value == "" {
		return ""
	}
	return name + "=" + sanitizeRPCParamValue(value)
}

func scalarParam(name string, raw json.RawMessage) string {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}

	if value, ok := jsonString(raw); ok {
		return name + "=" + sanitizeRPCParamValue(value)
	}

	var boolValue bool
	if err := json.Unmarshal(raw, &boolValue); err == nil {
		return fmt.Sprintf("%s=%t", name, boolValue)
	}

	var number json.Number
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err == nil {
		return name + "=" + sanitizeRPCParamValue(number.String())
	}

	return ""
}

func summarizeOpaqueParam(name string, raw json.RawMessage) string {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}

	if value, ok := jsonString(raw); ok {
		return summarizeOpaqueValue(name, []byte(value))
	}
	return summarizeOpaqueValue(name, raw)
}

func positionalStringParam(name string, params []json.RawMessage, index int) string {
	if index >= len(params) {
		return ""
	}
	return stringParam(name, params[index])
}

func positionalScalarParam(name string, params []json.RawMessage, index int) string {
	if index >= len(params) {
		return ""
	}
	return scalarParam(name, params[index])
}

func positionalOpaqueParam(name string, params []json.RawMessage, index int) string {
	if index >= len(params) {
		return ""
	}
	return summarizeOpaqueParam(name, params[index])
}

func summarizeRawJSON(raw []byte) string {
	return summarizeOpaqueValue("json", raw)
}

func summarizeOpaqueValue(name string, value []byte) string {
	sum := sha256.Sum256(value)
	return fmt.Sprintf("%s_len=%d %s_sha256=%s", name, len(value), name, hex.EncodeToString(sum[:])[:16])
}

func jsonString(raw json.RawMessage) (string, bool) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func joinParamParts(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	if len(kept) == 0 {
		return "-"
	}
	return strings.Join(kept, " ")
}

func sanitizeRPCParamValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}

	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case strings.ContainsRune("/._:-=,+", r):
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= 256 {
			break
		}
	}
	if b.Len() == 0 {
		return "-"
	}
	return b.String()
}

func bytesTrimSpace(value []byte) []byte {
	return []byte(strings.TrimSpace(string(value)))
}

func logRPCMirrorLine(r *http.Request, method, params string, batchSize int) {
	timestamp := r.Header.Get("X-Original-Time")
	if timestamp == "" {
		timestamp = time.Now().Format(time.RFC3339)
	}

	remote := valueOrDash(r.Header.Get("X-Original-Remote-Addr"))
	whitelist := valueOrDefault(r.Header.Get("X-Original-Whitelist"), "EXT")
	requestID := valueOrDash(r.Header.Get("X-Request-ID"))
	originalRequest := valueOrDash(r.Header.Get("X-Original-Request"))
	referer := valueOrDash(r.Header.Get("X-Original-Referer"))
	userAgent := valueOrDash(r.Header.Get("X-Original-User-Agent"))

	log.Printf(
		"[%s]\t%s\t[%s]\t[CHAINRPC]\t[*]\t%q 0 0 %q %q rt=- uct=\"-\" uht=\"-\" urt=\"-\" request_id=%q rpc_method=%q rpc_batch_size=%d rpc_params=%q",
		timestamp,
		remote,
		whitelist,
		originalRequest,
		referer,
		userAgent,
		requestID,
		method,
		batchSize,
		params,
	)
}

func valueOrDash(value string) string {
	return valueOrDefault(value, "-")
}

func valueOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func (bm *BanManager) ProcessEntry(entry AccessLogLine) {
	// 1. Check Status Code Scoring
	weight, defined := bm.scoreWeights[entry.Status]
	if !defined {
		return
	}
	score := weight

	ip := entry.RemoteAddr
	// Security: Strict validation of IP address to prevent injections in Nginx config
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		// Only valid IPs are allowed. Invalid strings, hostnames, or injections are dropped.
		return
	}

	bm.mu.Lock()
	defer bm.mu.Unlock()

	// 2. PRECEDENCE: Do not ban Whitelisted IPs
	if bm.whitelist[ip] {
		return
	}

	// Check Private / Trusted IPs
	if isPrivateIP(parsedIP) {
		return
	}
	for _, trusted := range bm.trustedProxies {
		if trusted.Contains(parsedIP) {
			return
		}
	}

	// 3. Already Banned?
	if _, banned := bm.bannedIPs[ip]; banned {
		return
	}

	// 4. Update Score
	// Memory Safety: Check if map is too large
	if len(bm.scores) > 100000 {
		// Evict 25% of random entries instead of full reset (Botnet mitigation)
		logBan("Safety limit reached (100k IPs). Evicting 25%% of scores to relieve pressure.")
		itemsToRemove := 25000
		count := 0
		for k := range bm.scores {
			delete(bm.scores, k)
			delete(bm.scoreLastSeen, k)
			count++
			if count >= itemsToRemove {
				break
			}
		}
	}

	bm.scores[ip] += score
	bm.scoreLastSeen[ip] = time.Now()
	current := bm.scores[ip]

	if current >= bm.maxRetries {
		bm.banIPLocked(ip, fmt.Sprintf("Score %d (Last: %d)", current, entry.Status))
	}
}

func (bm *BanManager) banIPLocked(ip, reason string) {
	expiration := time.Now().Add(bm.banDuration)
	bm.bannedIPs[ip] = expiration
	delete(bm.scores, ip) // Reset score
	logMsg("BanManager: BANNING %s until %s (Reason: %s)", ip, expiration.Format(time.RFC3339), reason)

	// Flush to disk
	bm.requestFlush()
}

func (bm *BanManager) startExpirer() {
	ticker := time.NewTicker(1 * time.Minute)
	for range ticker.C {
		bm.mu.Lock()
		now := time.Now()
		dirty := false
		for ip, exp := range bm.bannedIPs {
			if now.After(exp) {
				logBan("Unbanning %s (Expired)", ip)
				delete(bm.bannedIPs, ip)
				dirty = true
			}
		}

		// Cleanup stale scores
		for ip, lastSeen := range bm.scoreLastSeen {
			if now.Sub(lastSeen) > bm.scoreTTL {
				delete(bm.scores, ip)
				delete(bm.scoreLastSeen, ip)
			}
		}
		bm.mu.Unlock()

		if dirty {
			bm.requestFlush()
		}
	}
}

func (bm *BanManager) requestFlush() {
	select {
	case bm.flushChan <- struct{}{}:
	default:
	}
}

func (bm *BanManager) flushWorker() {
	debounceDuration := 2 * time.Second
	for range bm.flushChan {
		// Wait for more events
		time.Sleep(debounceDuration)

		// Drain
	drain:
		for {
			select {
			case <-bm.flushChan:
			default:
				break drain
			}
		}

		bm.flushBlacklist()
	}
}

func (bm *BanManager) flushBlacklist() {
	bm.mu.RLock()
	var banned []string
	for ip := range bm.bannedIPs {
		banned = append(banned, ip)
	}
	bm.mu.RUnlock()

	// Sort for stability
	sort.Strings(banned)

	var sb strings.Builder
	sb.WriteString("# Automatically generated by BanManager\n")
	sb.WriteString("geo $is_banned {\n")
	sb.WriteString("    default 0;\n")
	for _, ip := range banned {
		sb.WriteString(fmt.Sprintf("    %s 1;\n", ip))
	}
	sb.WriteString("}\n")

	// Atomic Write
	newContent := sb.String()
	tmpFile, err := os.CreateTemp("/etc/nginx/conf.d", "blacklist_tmp_*")
	if err != nil {
		logBan("Failed to create temp file: %v", err)
		return
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.WriteString(newContent); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		logBan("Failed to write config: %v", err)
		return
	}
	tmpFile.Close()

	if err := os.Rename(tmpPath, BlacklistConfigPath); err != nil {
		os.Remove(tmpPath)
		logBan("Failed to rename config: %v", err)
		return
	}
	os.Chmod(BlacklistConfigPath, 0644)

	// Trigger Reload
	logBan("Blacklist updated (%d IPs). Requesting reload.", len(banned))
	GlobalReloadManager.RequestReload()
}

// Global ReloadManager
// var GlobalReloadManager *ReloadManager <-- Removed Duplicate

// ReloadManager handles Nginx reloads with debouncing
type ReloadManager struct {
	trigger  chan struct{}
	debounce time.Duration
}

func NewReloadManager(debounce time.Duration) *ReloadManager {
	rm := &ReloadManager{
		trigger:  make(chan struct{}, 1),
		debounce: debounce,
	}
	go rm.run()
	return rm
}

func (rm *ReloadManager) RequestReload() {
	select {
	case rm.trigger <- struct{}{}:
	default:
		// Already scheduled
	}
}

func (rm *ReloadManager) run() {
	for range rm.trigger {
		// Wait for quiet period (Debounce)
		logReload("Change detected, buffering for %v...", rm.debounce)
		time.Sleep(rm.debounce)

		// Drain any events that came in during sleep
	drain:
		for {
			select {
			case <-rm.trigger:
			default:
				break drain
			}
		}

		// Perform reload
		logReload("Triggering Nginx reload...")
		cmd := exec.Command("nginx", "-s", "reload")
		if output, err := cmd.CombinedOutput(); err != nil {
			logReload("Nginx reload failed: %s", string(output))
		} else {
			logReload("Nginx reload success.")
		}
	}
}

// API Response Structures

// EpochResponse matches /v1/epochs/latest (Only used for Targets now)
type EpochResponse struct {
	EpochStages     EpochStages `json:"epoch_stages"`
	NextEpochStages EpochStages `json:"next_epoch_stages"`
}

type EpochStages struct {
	SetNewValidators int64 `json:"set_new_validators"`
}

// Node RPC Response Structures (matches /status)
type RPCStatusResponse struct {
	Result RPCResult `json:"result"`
}

type RPCResult struct {
	SyncInfo SyncInfo `json:"sync_info"`
}

type SyncInfo struct {
	LatestBlockHeight string `json:"latest_block_height"`
	LatestBlockTime   string `json:"latest_block_time"`
}

// ParticipantsResponse matches /v1/epochs/current/participants
type ParticipantsResponse struct {
	ActiveParticipants ActiveParticipantGroup `json:"active_participants"`
}

type ActiveParticipantGroup struct {
	Participants []Participant `json:"participants"`
}

type Participant struct {
	InferenceUrl string `json:"inference_url"`
	Address      string `json:"address"`
}

// State tracking
type State struct {
	BlockAvgDuration   time.Duration // Moving average of block time
	BlockHeightChecked int64         // The last block height we successfully checked/saw
	BlockTimeChecked   time.Time     // The timestamp of the last block we checked
	BlockHeightSynced  int64         // The 'SetNewValidators' height that we last successfully synced for
	BlockTimeSynced    time.Time     // The block time when we performed the last sync
}

func main() {
	// Disable standard flags, we handle timestamp manually
	log.SetFlags(0)
	logSys("Starting Dynamic Validator Whitelist Sync...")

	// 0. Initialize Reload Manager (Singleton)
	// Handles requests from both Whitelist Syncer and Fail2Ban
	GlobalReloadManager = NewReloadManager(5 * time.Second)

	rpcMethodLoggingEnabled = !strings.EqualFold(strings.TrimSpace(os.Getenv("DISABLE_RPC_METHOD_LOGGING")), "true")
	if rpcMethodLoggingEnabled {
		logRPC("Chain RPC method logging enabled.")
	} else {
		logSys("Chain RPC method logging disabled (DISABLE_RPC_METHOD_LOGGING=true).")
	}
	go startRPCMethodLogServer()

	// 0b. Initialize Ban Manager (Fail2Ban-style IP bans from nginx JSON access logs)
	// Same semantics as DISABLE_CHAIN_*: unset or "true" = off; "false" = on.
	if !proxyFeatureDisabled("DISABLE_FAIL2BAN") {
		banDurStr := os.Getenv("FAIL2BAN_BAN_DURATION")
		if banDurStr == "" {
			banDurStr = "10m"
		}
		banDur, err := time.ParseDuration(banDurStr)
		if err != nil {
			logBan("Invalid FAIL2BAN_BAN_DURATION, defaulting to 10m: %v", err)
			banDur = 10 * time.Minute
		}

		maxRetriesStr := os.Getenv("FAIL2BAN_MAX_RETRIES")
		maxRetries := 50
		if maxRetriesStr != "" {
			if val, err := strconv.Atoi(maxRetriesStr); err == nil {
				maxRetries = val
			}
		}

		// Parse Weights
		weights := make(map[int]int)
		weights[401] = getEnvInt("FAIL2BAN_SCORE_401", 5)
		weights[403] = getEnvInt("FAIL2BAN_SCORE_403", 5)
		weights[400] = getEnvInt("FAIL2BAN_SCORE_400", 2)

		logBan("Initializing BanManager (Duration: %v, Threshold: %d pts, Weights: %v)", banDur, maxRetries, weights)
		GlobalBanManager = NewBanManager(banDur, maxRetries, weights)
		go tailLogs()
	} else {
		logSys("Fail2Ban-style banning is disabled (unset or DISABLE_FAIL2BAN=true; set DISABLE_FAIL2BAN=false to enable).")
	}

	// Always start Log Rotator (Nginx writes JSON logs regardless of Fail2Ban)
	go startLogRotator()

	// 1. Config

	// 1. Config
	apiHost := os.Getenv("FINAL_API_SERVICE")
	apiPort := os.Getenv("GONKA_API_PORT")
	if apiHost == "" {
		apiHost = "127.0.0.1"
	}
	if apiPort == "" {
		apiPort = "9000"
	}
	ApiUrl = fmt.Sprintf("http://%s:%s", apiHost, apiPort)
	logSys("Configured API URL: %s", ApiUrl)

	// Node RPC Config
	rpcHost := os.Getenv("FINAL_NODE_SERVICE")
	rpcPort := os.Getenv("CHAIN_RPC_PORT")
	if rpcHost == "" {
		rpcHost = "127.0.0.1"
	}
	if rpcPort == "" {
		rpcPort = "26657"
	}
	NodeRPCUrl = fmt.Sprintf("http://%s:%s", rpcHost, rpcPort)
	logSys("Configured Node RPC URL: %s", NodeRPCUrl)

	if !validatorWhitelistEnabled() {
		logSys("Validator IP whitelist sync disabled (unset or DISABLE_VALIDATOR_WHITELIST=true; set DISABLE_VALIDATOR_WHITELIST=false to enable).")
		select {}
	}

	// Initial State
	state := State{
		BlockAvgDuration:  DefaultBlockTime,
		BlockHeightSynced: 0,
	}

	// 2. Initial Load
	// We perform an initial sync regardless of epoch state to ensure Nginx has a config.
	logMsg("Performing initial whitelist sync...")
	if err := syncWhitelist(); err != nil {
		logMsg("Initial sync failed (will retry in loop): %v", err)
	} else {
		// If success, try to initialize BlockHeightSynced.
		// We need Epoch Info for the target.
		if epochResp, err := fetchEpochInfo(); err == nil {
			// We need Current Block Height to see if we are past the target.
			// Let's use RPC for this if available, or just skip optimization for first run.
			if currentHeight, currentTime, err := fetchNodeStatus(); err == nil {
				if currentHeight >= epochResp.EpochStages.SetNewValidators {
					state.BlockHeightSynced = epochResp.EpochStages.SetNewValidators
					state.BlockTimeSynced = currentTime
				}
			}
		}
	}

	// 3. Adaptive Loop
	for {
		// Step A: Fetch Current Chain State (Height/Time) from RPC
		currentHeight, currentTime, err := fetchNodeStatus()
		if err != nil {
			logMsg("Failed to get node status: %v. Retrying in %v...", err, ErrorWaitTime)
			time.Sleep(ErrorWaitTime)
			continue
		}

		// Step B: Fetch Epoch Targets from API
		epochResp, err := fetchEpochInfo()
		if err != nil {
			logMsg("Failed to get epoch info: %v. Retrying in %v...", err, ErrorWaitTime)
			time.Sleep(ErrorWaitTime)
			continue
		}

		// Step C: Update Block Time Logic
		updateBlockAvgDuration(&state, currentHeight, currentTime)

		// Step D: Check Sync Condition
		// We only sync if:
		// 1. We have passed (or met) the SetNewValidators block height.
		// 2. We haven't already synced for this specific SetNewValidators height.
		currentSetTarget := epochResp.EpochStages.SetNewValidators

		if currentHeight >= currentSetTarget {
			if currentSetTarget > state.BlockHeightSynced {
				logMsg("New Validator Set active (Current: %d, Target: %d). Syncing whitelist...", currentHeight, currentSetTarget)
				if err := syncWhitelist(); err != nil {
					logMsg("Sync failed: %v", err)
					time.Sleep(ErrorWaitTime)
					continue
				} else {
					state.BlockHeightSynced = currentSetTarget
					state.BlockTimeSynced = currentTime
					logMsg("Sync complete. Updated BlockHeightSynced to %d (BlockTime: %s)", state.BlockHeightSynced, state.BlockTimeSynced.Format(time.RFC3339))
				}
			}
		}

		// Step E: Calculate Sleep Time
		waitTime := calculateWait(&state, currentHeight, epochResp)
		logMsg("Sleeping for %v...", waitTime)
		time.Sleep(waitTime)
	}
}

// Helper for consistent log prefix and timestamp
// Helper for consistent log prefix and timestamp
func logTagged(tag, format string, v ...interface{}) {
	timestamp := time.Now().Format(time.RFC3339)
	prefix := fmt.Sprintf("[%s] [PROXY - %s] ", timestamp, tag)
	log.Printf(prefix+format, v...)
}

func logMsg(format string, v ...interface{}) {
	logTagged("WHITELIST", format, v...)
}

func logBan(format string, v ...interface{}) {
	logTagged("FAIL2BAN", format, v...)
}

func logReload(format string, v ...interface{}) {
	logTagged("RELOAD", format, v...)
}

func logRPC(format string, v ...interface{}) {
	logTagged("RPC", format, v...)
}

func logSys(format string, v ...interface{}) {
	logTagged("SYSTEM", format, v...)
}

func updateBlockAvgDuration(state *State, currentHeight int64, currentTime time.Time) {
	// If this is the first real check, just initialize
	if state.BlockHeightChecked == 0 {
		state.BlockHeightChecked = currentHeight
		state.BlockTimeChecked = currentTime
		return
	}

	if currentHeight > state.BlockHeightChecked {
		blocksPassed := currentHeight - state.BlockHeightChecked
		timePassed := currentTime.Sub(state.BlockTimeChecked)

		if blocksPassed > 0 && timePassed > 0 {
			latestAvg := timePassed / time.Duration(blocksPassed)
			// Smoothing (80% old, 20% new)
			state.BlockAvgDuration = (state.BlockAvgDuration*4 + latestAvg) / 5
			logMsg("Updated BlockAvgDuration to %v (based on %d blocks in %v)", state.BlockAvgDuration, blocksPassed, timePassed)
		}
	}
	state.BlockHeightChecked = currentHeight
	state.BlockTimeChecked = currentTime
}

func calculateWait(state *State, currentHeight int64, resp *EpochResponse) time.Duration {
	targetHeight := resp.EpochStages.SetNewValidators
	blocksRemaining := targetHeight - currentHeight

	// Logic:
	// 1. If we are BEFORE SetNewValidators, target it.
	// 2. If we are PAST SetNewValidators, target Next Epoch.

	if blocksRemaining <= 0 {
		logMsg("Current SetNewValidators passed (%d vs %d). Targeting Next Epoch.", targetHeight, currentHeight)
		targetHeight = resp.NextEpochStages.SetNewValidators
		blocksRemaining = targetHeight - currentHeight
		logMsg("New Target (Next Epoch): %d (%d blocks away)", targetHeight, blocksRemaining)
	} else {
		logMsg("Targeting Current Epoch SetNewValidators: %d (%d blocks away)", targetHeight, blocksRemaining)
	}

	if blocksRemaining <= 0 {
		logMsg("Blocks remaining %d (Past/Active) even after checking Next Epoch. Fallback to min poll.", blocksRemaining)
		return PollMinInterval
	}

	estimatedDuration := time.Duration(blocksRemaining) * state.BlockAvgDuration

	if estimatedDuration > PollMaxInterval {
		return PollMaxInterval
	}

	if estimatedDuration < 5*time.Minute {
		return PollMinInterval
	}

	// Buffer
	return estimatedDuration - time.Minute
}

// fetchNodeStatus gets current block height and time from Tendermint RPC
func fetchNodeStatus() (int64, time.Time, error) {
	client := http.Client{Timeout: ApiTimeout}
	resp, err := client.Get(NodeRPCUrl + "/status")
	if err != nil {
		return 0, time.Time{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, time.Time{}, fmt.Errorf("bad status from RPC: %s", resp.Status)
	}

	var statusResp RPCStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&statusResp); err != nil {
		return 0, time.Time{}, err
	}

	// Parse Height
	height, err := strconv.ParseInt(statusResp.Result.SyncInfo.LatestBlockHeight, 10, 64)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("invalid block height: %v", err)
	}

	// Parse Time
	blockTime, err := time.Parse(time.RFC3339Nano, statusResp.Result.SyncInfo.LatestBlockTime)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("invalid block time: %v", err)
	}

	return height, blockTime, nil
}

func fetchEpochInfo() (*EpochResponse, error) {
	client := http.Client{Timeout: ApiTimeout}
	resp, err := client.Get(ApiUrl + "/v1/epochs/latest")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad status: %s", resp.Status)
	}

	var epochResp EpochResponse
	if err := json.NewDecoder(resp.Body).Decode(&epochResp); err != nil {
		return nil, err
	}
	return &epochResp, nil
}

func syncWhitelist() error {
	logMsg("Syncing whitelist...")

	// 1. Fetch Participants
	client := http.Client{Timeout: ApiTimeout}
	resp, err := client.Get(ApiUrl + "/v1/epochs/current/participants")
	if err != nil {
		return fmt.Errorf("fetch participants failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return fmt.Errorf("participants endpoint returned 404 (Not Found) - preserving existing whitelist state")
		}
		return fmt.Errorf("bad status fetching participants: %s", resp.Status)
	}

	var pResp ParticipantsResponse
	if err := json.NewDecoder(resp.Body).Decode(&pResp); err != nil {
		return fmt.Errorf("decode participants failed: %w", err)
	}

	// 2. Extract and Resolve IPs (Concurrent)
	whitelistMap := make(map[string]bool)
	var mutex sync.Mutex
	resolver := net.Resolver{}

	// Stats tracking (Atomic)
	var totalParticipants int64

	participants := pResp.ActiveParticipants.Participants
	totalParticipants = int64(len(participants))

	// Worker Pool for DNS Resolution
	concurrency := 20
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for _, p := range participants {
		wg.Add(1)
		sem <- struct{}{} // Acquire token
		go func(p Participant) {
			defer wg.Done()
			defer func() { <-sem }() // Release token

			if p.InferenceUrl == "" {
				// thread-safe counter increment
				// casting for atomic not strictly needed for stats, but cleaner to just use mutex or loose stats
				// keeping simple with mutex for map, loose for stats is fine or use atomic.
				// upgrading stats to atomic for correctness
				return // skippedUrl implicitly
			}

			cleanUrl := p.InferenceUrl
			if !strings.HasPrefix(strings.ToLower(cleanUrl), "http") {
				cleanUrl = "http://" + cleanUrl
			}

			u, err := url.Parse(cleanUrl)
			if err != nil {
				logMsg("Warning - skipping invalid url %s: %v", p.InferenceUrl, err)
				return // skippedUrl
			}

			host := u.Hostname()
			var ips []net.IP

			if ip := net.ParseIP(host); ip != nil {
				ips = []net.IP{ip}
			} else {
				func() {
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					resolvedIPs, err := resolver.LookupIPAddr(ctx, host)
					if err != nil {
						logMsg("Warning - could not resolve %s: %v", host, err)
						return // skippedResolution
					}
					for _, ipAddr := range resolvedIPs {
						ips = append(ips, ipAddr.IP)
					}
				}()
			}

			if len(ips) > 0 {
				mutex.Lock()
				for _, ip := range ips {
					if isPrivateIP(ip) {
						// skippedPrivate
						continue
					}
					whitelistMap[ip.String()] = true
				}
				mutex.Unlock()
			}
		}(p)
	}
	wg.Wait()

	var allowed []string
	for ip := range whitelistMap {
		allowed = append(allowed, ip)
	}

	sort.Strings(allowed)

	logMsg("Found %d unique public IPs to whitelist (Total scanned: %d).",
		len(allowed), totalParticipants)

	// Update In-Memory BanManager (so it doesn't ban these IPs)
	if GlobalBanManager != nil {
		GlobalBanManager.UpdateWhitelist(allowed)
	}

	return updateNginxConfig(allowed)
}

func updateNginxConfig(ips []string) error {
	// Generate config content
	var sb strings.Builder
	sb.WriteString("# Automatically generated by gonka-proxy-sidecar\n")
	sb.WriteString("# Do not edit manually\n\n")

	// nginx's geo module does NOT expand variables in values.
	// Use geo for 0/1 classification, then map to expand $binary_remote_addr.
	sb.WriteString("geo $whitelist_class {\n")
	sb.WriteString("    default 0;\n")
	for _, ip := range ips {
		sb.WriteString(fmt.Sprintf("    %s 1;\n", ip))
	}
	sb.WriteString("}\n\n")

	sb.WriteString("map $whitelist_class $whitelist_limit_key {\n")
	sb.WriteString("    0 $binary_remote_addr;\n")
	sb.WriteString("    1 \"\";\n")
	sb.WriteString("}\n\n")

	sb.WriteString("geo $whitelist_log_type {\n")
	sb.WriteString("    default \"EXT\";\n")
	for _, ip := range ips {
		sb.WriteString(fmt.Sprintf("    %s \"INT\";\n", ip))
	}
	sb.WriteString("}\n")

	newContent := sb.String()

	// Check if changed
	currentContent, _ := os.ReadFile(NginxConfigPath)
	if string(currentContent) == newContent {
		log.Println("Sidecar: Configuration unchanged. Skipping reload.")
		return nil
	}

	// Atomically Write File
	tmpFile, err := os.CreateTemp("/etc/nginx/conf.d", "whitelist_tmp_*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.WriteString(newContent); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("failed to write to temp file: %w", err)
	}
	tmpFile.Close()

	if err := os.Rename(tmpPath, NginxConfigPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename temp file to config path: %w", err)
	}

	os.Chmod(NginxConfigPath, 0644)
	logSys("Configuration updated. Requesting Reload.")

	// Request Reload via Manager (Non-blocking, Debounced)
	GlobalReloadManager.RequestReload()
	return nil
}

// isPrivateIP checks if an IP is private or loopback
func isPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}

	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 10:
			return true
		case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
			return true
		case ip4[0] == 192 && ip4[1] == 168:
			return true
		}
		return false
	}

	if len(ip) == net.IPv6len {
		return (ip[0] & 0xfe) == 0xfc
	}

	return false
}

// getEnvInt reads an environment variable as an integer or returns a default
func getEnvInt(key string, defaultVal int) int {
	if valStr := os.Getenv(key); valStr != "" {
		if val, err := strconv.Atoi(valStr); err == nil {
			return val
		}
	}
	return defaultVal
}

func validatorWhitelistEnabled() bool {
	return !proxyFeatureDisabled("DISABLE_VALIDATOR_WHITELIST")
}

// proxyFeatureDisabled matches entrypoint DISABLE_CHAIN_* behavior: empty or "true" means disabled;
// only explicit "false" turns the feature on.
func proxyFeatureDisabled(key string) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return true
	}
	return strings.EqualFold(v, "true")
}

func tailLogs() {
	for {
		logSys("Starting access log tailer on %s", LogFilePath)
		cmd := exec.Command("tail", "-n", "0", "-F", LogFilePath)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			logSys("tailLogs: stdout pipe error: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}

		if err := cmd.Start(); err != nil {
			logSys("tailLogs: start error: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}

		scanner := bufio.NewScanner(stdout)
		buf := make([]byte, 0, 1024*1024) // 1MB buffer
		scanner.Buffer(buf, 1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			GlobalBanManager.ProcessLogLine(line)
		}

		// Ensure tail is killed if scanner errors/stops, so Wait() doesn't block
		if cmd.Process != nil {
			cmd.Process.Kill()
		}

		if err := scanner.Err(); err != nil {
			logSys("tailLogs: scanner error: %v", err)
		}

		if err := cmd.Wait(); err != nil {
			logSys("tailLogs: command exited: %v", err)
		}

		logSys("tailLogs: process exited. Restarting in 5 seconds...")
		time.Sleep(5 * time.Second)
	}
}

// Log Rotation Manager
func startLogRotator() {
	ticker := time.NewTicker(1 * time.Minute)
	limit := int64(100 * 1024 * 1024) // 100MB

	for range ticker.C {
		info, err := os.Stat(LogFilePath)
		if err != nil {
			continue
		}

		if info.Size() > limit {
			logSys("LogRotator: Log file size %d MB exceeds limit. Rotating...", info.Size()/1024/1024)
			rotateLogs()
		}
	}
}

func rotateLogs() {
	// 1. Rename current log
	backupName := LogFilePath + ".old"
	if err := os.Rename(LogFilePath, backupName); err != nil {
		logSys("LogRotator: Rename failed: %v", err)
		return
	}

	// 2. Signal Nginx to reopen log files (USR1 equivalent)
	cmd := exec.Command("nginx", "-s", "reopen")
	if output, err := cmd.CombinedOutput(); err != nil {
		logSys("LogRotator: Reopen failed: %s", string(output))
	} else {
		logSys("LogRotator: Logs reopened.")
	}

	// 3. Delete old log
	if err := os.Remove(backupName); err != nil {
		logSys("LogRotator: Delete old log failed: %v", err)
	}
}
