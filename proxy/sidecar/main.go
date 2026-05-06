package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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

// Constants for Fail2Ban
const (
	BlacklistConfigPath = "/etc/nginx/conf.d/blacklist_ips.conf"
	LogFilePath         = "/var/log/nginx/access_json.log"
)

// Global Managers
var (
	GlobalReloadManager *ReloadManager
	GlobalBanManager    *BanManager
)

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

		// Start Log Watcher in background
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

	sb.WriteString("geo $whitelist_limit_key {\n")
	sb.WriteString("    default $binary_remote_addr;\n")

	for _, ip := range ips {
		sb.WriteString(fmt.Sprintf("    %s \"\";\n", ip))
	}
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
		logBan("Starting log tailer on %s", LogFilePath)
		cmd := exec.Command("tail", "-n", "0", "-F", LogFilePath)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			logBan("tailLogs: stdout pipe error: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}

		if err := cmd.Start(); err != nil {
			logBan("tailLogs: start error: %v", err)
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
			logBan("tailLogs: scanner error: %v", err)
		}

		if err := cmd.Wait(); err != nil {
			logBan("tailLogs: command exited: %v", err)
		}

		logBan("tailLogs: process exited. Restarting in 5 seconds...")
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
