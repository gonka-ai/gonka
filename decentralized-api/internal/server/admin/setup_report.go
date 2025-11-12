package admin

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/cosmosclient"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference"
	"github.com/productscience/inference/x/inference/types"
)

// Type Definitions

type SetupReport struct {
	Experimental  bool          `json:"experimental"`
	GeneratedAt   time.Time     `json:"generated_at"`
	CachedUntil   time.Time     `json:"cached_until"`
	OverallStatus CheckStatus   `json:"overall_status"`
	Checks        []Check       `json:"checks"`
	Summary       ReportSummary `json:"summary"`
}

type Check struct {
	ID      string      `json:"id"`
	Status  CheckStatus `json:"status"`
	Message string      `json:"message"`
	Details interface{} `json:"details,omitempty"`
}

type CheckStatus string

const (
	PASS        CheckStatus = "PASS"
	FAIL        CheckStatus = "FAIL"
	UNAVAILABLE CheckStatus = "UNAVAILABLE"
)

type ReportSummary struct {
	TotalChecks       int      `json:"total_checks"`
	PassedChecks      int      `json:"passed_checks"`
	FailedChecks      int      `json:"failed_checks"`
	UnavailableChecks int      `json:"unavailable_checks"`
	Issues            []string `json:"issues"`
	Recommendations   []string `json:"recommendations"`
}

type GPUDeviceInfo struct {
	Index         int     `json:"index"`
	Name          string  `json:"name"`
	TotalMemoryGB float64 `json:"total_memory_gb"`
	FreeMemoryGB  float64 `json:"free_memory_gb"`
	UsedMemoryGB  float64 `json:"used_memory_gb"`
	Utilization   *int    `json:"utilization_percent,omitempty"`
	Temperature   *int    `json:"temperature_c,omitempty"`
	Available     bool    `json:"available"`
	Error         *string `json:"error,omitempty"`
}

// Cache Management

var (
	cachedReport      *SetupReport
	cachedReportMutex sync.RWMutex
)

// EXPERIMENTAL: Setup report endpoint for participant onboarding validation
func (s *Server) getSetupReport(c echo.Context) error {
	ctx := c.Request().Context()

	cachedReportMutex.RLock()
	if cachedReport != nil && time.Now().Before(cachedReport.CachedUntil) {
		report := cachedReport
		cachedReportMutex.RUnlock()
		return c.JSON(http.StatusOK, report)
	}
	cachedReportMutex.RUnlock()

	report, err := s.generateSetupReport(ctx)
	if err != nil {
		if report != nil {
			return c.JSON(http.StatusOK, report)
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	cachedReportMutex.Lock()
	cachedReport = report
	cachedReportMutex.Unlock()

	return c.JSON(http.StatusOK, report)
}

func (s *Server) generateSetupReport(ctx context.Context) (*SetupReport, error) {
	now := time.Now()
	report := &SetupReport{
		Experimental: true,
		GeneratedAt:  now,
		CachedUntil:  now.Add(1 * time.Minute),
		Checks:       []Check{},
	}

	checks := []Check{}
	checks = append(checks, s.checkColdKey(ctx)...)
	checks = append(checks, s.checkWarmKey(ctx)...)
	checks = append(checks, s.checkPermissions(ctx))
	checks = append(checks, s.checkConsensusKey(ctx)...)
	checks = append(checks, s.checkParticipant(ctx))
	checks = append(checks, s.checkMLNodes(ctx)...)
	checks = append(checks, s.checkBlockSync(ctx))

	report.Checks = checks
	s.generateSummary(report)

	return report, nil
}

func (s *Server) checkColdKey(ctx context.Context) []Check {
	checks := []Check{}

	nodeConfig := s.configManager.GetChainNodeConfig()
	pubKey := s.recorder.GetAccountPubKey()
	address := s.recorder.GetAccountAddress()

	if pubKey == nil || address == "" || nodeConfig.AccountPublicKey == "" {
		checks = append(checks, Check{
			ID:      "cold_key_configured",
			Status:  FAIL,
			Message: "Cold key is not configured",
		})
	} else {
		checks = append(checks, Check{
			ID:      "cold_key_configured",
			Status:  PASS,
			Message: "Cold key is configured",
			Details: map[string]interface{}{
				"address": address,
				"pubkey":  nodeConfig.AccountPublicKey,
			},
		})
	}

	kr := s.recorder.GetKeyring()
	_, err := (*kr).Key(address)
	if err == nil {
		checks = append(checks, Check{
			ID:      "cold_key_not_in_keyring",
			Status:  FAIL,
			Message: "Cold key is in keyring (should only be in environment config)",
		})
	} else {
		checks = append(checks, Check{
			ID:      "cold_key_not_in_keyring",
			Status:  PASS,
			Message: "Cold key correctly NOT in keyring",
		})
	}

	return checks
}

func (s *Server) checkWarmKey(ctx context.Context) []Check {
	checks := []Check{}

	nodeConfig := s.configManager.GetChainNodeConfig()
	keyName := nodeConfig.SignerKeyName
	kr := s.recorder.GetKeyring()

	keyRecord, err := (*kr).Key(keyName)
	if err != nil {
		checks = append(checks, Check{
			ID:      "warm_key_in_keyring",
			Status:  FAIL,
			Message: fmt.Sprintf("Warm key '%s' not found in keyring: %s", keyName, err.Error()),
		})
		return checks
	}

	checks = append(checks, Check{
		ID:      "warm_key_in_keyring",
		Status:  PASS,
		Message: fmt.Sprintf("Warm key '%s' found in keyring", keyName),
	})

	pubKey, err := keyRecord.GetPubKey()
	if err != nil {
		checks = append(checks, Check{
			ID:      "warm_key_address_match",
			Status:  UNAVAILABLE,
			Message: fmt.Sprintf("Failed to get public key from keyring: %s", err.Error()),
		})
		return checks
	}

	apiAccount := s.recorder.GetApiAccount()
	addr, err := sdk.Bech32ifyAddressBytes(apiAccount.AddressPrefix, pubKey.Address())
	if err != nil {
		checks = append(checks, Check{
			ID:      "warm_key_address_match",
			Status:  UNAVAILABLE,
			Message: fmt.Sprintf("Failed to convert address: %s", err.Error()),
		})
		return checks
	}

	signerAddr := s.recorder.GetSignerAddress()
	if addr != signerAddr {
		checks = append(checks, Check{
			ID:      "warm_key_address_match",
			Status:  FAIL,
			Message: fmt.Sprintf("Warm key address mismatch: keyring=%s, signer=%s", addr, signerAddr),
		})
	} else {
		checks = append(checks, Check{
			ID:      "warm_key_address_match",
			Status:  PASS,
			Message: "Warm key address matches signer address",
		})
	}

	return checks
}

func (s *Server) checkPermissions(ctx context.Context) Check {
	coldKeyAddr := s.recorder.GetAccountAddress()
	warmKeyAddr := s.recorder.GetSignerAddress()

	authzQueryClient := authztypes.NewQueryClient(s.recorder.GetClientContext())
	grantsResp, err := authzQueryClient.GranteeGrants(ctx, &authztypes.QueryGranteeGrantsRequest{
		Grantee: warmKeyAddr,
	})

	if err != nil {
		return Check{
			ID:      "permissions_granted",
			Status:  UNAVAILABLE,
			Message: fmt.Sprintf("Unable to check permissions: %s", err.Error()),
		}
	}

	grantedFromColdKey := make(map[string]*authztypes.GrantAuthorization)
	for _, grant := range grantsResp.Grants {
		if grant.Granter == coldKeyAddr {
			var authorization authztypes.Authorization
			if err := s.cdc.UnpackAny(grant.Authorization, &authorization); err != nil {
				continue
			}
			if genericAuth, ok := authorization.(*authztypes.GenericAuthorization); ok {
				grantedFromColdKey[genericAuth.Msg] = grant
			}
		}
	}

	requiredPerms := []string{}
	for _, msgType := range inference.InferenceOperationKeyPerms {
		requiredPerms = append(requiredPerms, sdk.MsgTypeURL(msgType))
	}

	missingPerms := []string{}
	expiringSoon := []string{}
	blockTime := time.Now()

	for _, msgTypeUrl := range requiredPerms {
		grant, found := grantedFromColdKey[msgTypeUrl]
		if !found {
			missingPerms = append(missingPerms, msgTypeUrl)
			continue
		}

		if grant.Expiration != nil && grant.Expiration.Before(blockTime.Add(7*24*time.Hour)) {
			expiringSoon = append(expiringSoon, msgTypeUrl)
		}
	}

	status := PASS
	message := fmt.Sprintf("All %d required permissions granted", len(requiredPerms))

	if len(missingPerms) > 0 {
		status = FAIL
		message = fmt.Sprintf("Missing %d of %d required permissions", len(missingPerms), len(requiredPerms))
	} else if len(expiringSoon) > 0 {
		message = fmt.Sprintf("All %d permissions granted, but %d expiring soon", len(requiredPerms), len(expiringSoon))
	}

	details := map[string]interface{}{
		"cold_key_address": coldKeyAddr,
		"warm_key_address": warmKeyAddr,
		"granted":          len(requiredPerms) - len(missingPerms),
		"missing":          len(missingPerms),
	}

	if len(missingPerms) > 0 {
		details["missing_permissions"] = missingPerms
	}

	if len(expiringSoon) > 0 {
		details["expiring_soon"] = expiringSoon
	}

	return Check{
		ID:      "permissions_granted",
		Status:  status,
		Message: message,
		Details: details,
	}
}

func (s *Server) checkConsensusKey(ctx context.Context) []Check {
	checks := []Check{}

	// Get consensus key from local node
	chainNodeUrl := s.configManager.GetChainNodeConfig().Url
	rpcClient, err := cosmosclient.NewRpcClient(chainNodeUrl)
	if err != nil {
		checks = append(checks, Check{
			ID:      "consensus_key_match",
			Status:  UNAVAILABLE,
			Message: fmt.Sprintf("Failed to connect to chain node: %s", err.Error()),
		})
		return checks
	}

	status, err := rpcClient.Status(context.Background())
	if err != nil {
		checks = append(checks, Check{
			ID:      "consensus_key_match",
			Status:  UNAVAILABLE,
			Message: fmt.Sprintf("Failed to get node status: %s", err.Error()),
		})
		return checks
	}

	consensusKey := status.ValidatorInfo.PubKey
	consensusKeyBase64 := base64.StdEncoding.EncodeToString(consensusKey.Bytes())

	// Get participant info
	myAddress := s.recorder.GetAccountAddress()
	queryClient := s.recorder.NewInferenceQueryClient()
	participantResp, err := queryClient.Participant(ctx, &types.QueryGetParticipantRequest{
		Index: myAddress,
	})

	if err != nil {
		checks = append(checks, Check{
			ID:      "consensus_key_match",
			Status:  UNAVAILABLE,
			Message: fmt.Sprintf("Failed to query participant: %s", err.Error()),
		})
		return checks
	}

	participant := participantResp.Participant

	// Check 1: Consensus key matches
	if participant.ValidatorKey != consensusKeyBase64 {
		checks = append(checks, Check{
			ID:      "consensus_key_match",
			Status:  FAIL,
			Message: fmt.Sprintf("Consensus key mismatch: local=%s, registered=%s", consensusKeyBase64, participant.ValidatorKey),
			Details: map[string]interface{}{
				"consensus_pubkey":  consensusKeyBase64,
				"registered_pubkey": participant.ValidatorKey,
			},
		})
	} else {
		checks = append(checks, Check{
			ID:      "consensus_key_match",
			Status:  PASS,
			Message: "Consensus key matches participant registration",
			Details: map[string]interface{}{
				"consensus_pubkey": consensusKeyBase64,
			},
		})
	}

	// Check 2: Active in current epoch
	currentEpochResp, err := queryClient.GetCurrentEpoch(ctx, &types.QueryGetCurrentEpochRequest{})
	if err != nil {
		checks = append(checks, Check{
			ID:      "active_in_epoch",
			Status:  UNAVAILABLE,
			Message: fmt.Sprintf("Failed to get current epoch: %s", err.Error()),
		})
	} else {
		currentEpoch := currentEpochResp.Epoch

		// Query active participants using store key
		dataKey := types.ActiveParticipantsFullKey(currentEpoch)
		result, err := cosmosclient.QueryByKey(rpcClient, "inference", dataKey)

		if err != nil || len(result.Response.Value) == 0 {
			checks = append(checks, Check{
				ID:      "active_in_epoch",
				Status:  UNAVAILABLE,
				Message: fmt.Sprintf("Failed to query active participants for epoch %d", currentEpoch),
			})
		} else {
			var activeParticipants types.ActiveParticipants
			if err := s.cdc.Unmarshal(result.Response.Value, &activeParticipants); err != nil {
				checks = append(checks, Check{
					ID:      "active_in_epoch",
					Status:  UNAVAILABLE,
					Message: fmt.Sprintf("Failed to unmarshal active participants: %s", err.Error()),
				})
			} else {
				myWeight := int64(0)
				isActive := false
				for _, ap := range activeParticipants.Participants {
					if ap.Index == myAddress {
						isActive = true
						myWeight = ap.Weight
						break
					}
				}

				if isActive {
					checks = append(checks, Check{
						ID:      "active_in_epoch",
						Status:  PASS,
						Message: fmt.Sprintf("Participant is active in epoch %d with weight %d", currentEpoch, myWeight),
						Details: map[string]interface{}{
							"epoch":  currentEpoch,
							"weight": myWeight,
						},
					})
				} else {
					checks = append(checks, Check{
						ID:      "active_in_epoch",
						Status:  FAIL,
						Message: fmt.Sprintf("Participant is NOT active in epoch %d", currentEpoch),
						Details: map[string]interface{}{
							"epoch": currentEpoch,
						},
					})
				}
			}
		}
	}

	// Check 3: Validator status - check if present in validator set
	// Query the current validator set to see if our consensus key is in it
	validatorSetResp, err := rpcClient.Validators(context.Background(), nil, nil, nil)
	if err != nil {
		checks = append(checks, Check{
			ID:      "validator_in_set",
			Status:  UNAVAILABLE,
			Message: fmt.Sprintf("Failed to query validator set: %s", err.Error()),
		})
	} else {
		// Check if our consensus key is in the validator set
		foundInSet := false
		for _, val := range validatorSetResp.Validators {
			if val.PubKey.Equals(consensusKey) {
				foundInSet = true
				break
			}
		}

		if foundInSet {
			checks = append(checks, Check{
				ID:      "validator_in_set",
				Status:  PASS,
				Message: "Validator is active in consensus validator set",
				Details: map[string]interface{}{
					"consensus_pubkey": consensusKeyBase64,
				},
			})
		} else {
			checks = append(checks, Check{
				ID:      "validator_in_set",
				Status:  FAIL,
				Message: "Validator is NOT in consensus validator set (may need to register or unjail)",
				Details: map[string]interface{}{
					"consensus_pubkey": consensusKeyBase64,
				},
			})
		}
	}

	return checks
}

func (s *Server) checkParticipant(ctx context.Context) Check {
	myAddress := s.recorder.GetAccountAddress()
	queryClient := s.recorder.NewInferenceQueryClient()
	participantResp, err := queryClient.Participant(ctx, &types.QueryGetParticipantRequest{
		Index: myAddress,
	})

	if err != nil {
		return Check{
			ID:      "missed_requests_threshold",
			Status:  UNAVAILABLE,
			Message: fmt.Sprintf("Unable to query participant stats: %s", err.Error()),
		}
	}

	stats := participantResp.Participant.CurrentEpochStats
	totalRequests := stats.InferenceCount + stats.MissedRequests

	missedPercentage := 0.0
	if totalRequests > 0 {
		missedPercentage = float64(stats.MissedRequests) / float64(totalRequests) * 100
	}

	status := PASS
	message := fmt.Sprintf("Missed requests: %.1f%% (below 10%% threshold)", missedPercentage)

	if missedPercentage >= 10.0 {
		status = FAIL
		message = fmt.Sprintf("Missed requests: %.1f%% (exceeds 10%% threshold)", missedPercentage)
	}

	return Check{
		ID:      "missed_requests_threshold",
		Status:  status,
		Message: message,
		Details: map[string]interface{}{
			"inference_count":   stats.InferenceCount,
			"missed_requests":   stats.MissedRequests,
			"total_requests":    totalRequests,
			"missed_percentage": missedPercentage,
		},
	}
}

func (s *Server) checkMLNodes(ctx context.Context) []Check {
	nodes := s.configManager.GetConfig().Nodes
	checks := []Check{}

	for _, node := range nodes {
		// Build PoC URL
		version := s.configManager.GetCurrentNodeVersion()
		pocUrl := getPoCUrlWithVersion(node, version)

		// Check health endpoint
		healthUrl, _ := url.JoinPath(pocUrl, "/health")

		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get(healthUrl)

		healthy := false
		errorMsg := ""
		if err != nil {
			errorMsg = err.Error()
		} else if resp.StatusCode == 200 {
			healthy = true
			resp.Body.Close()
		} else {
			errorMsg = fmt.Sprintf("HTTP %d", resp.StatusCode)
			resp.Body.Close()
		}

		// Query GPU devices
		gpuDevices := []GPUDeviceInfo{}
		gpuUrl, _ := url.JoinPath(pocUrl, "/api/v1/gpu/devices")
		gpuResp, err := client.Get(gpuUrl)
		if err == nil && gpuResp.StatusCode == 200 {
			var gpuData struct {
				Devices []struct {
					Index              int     `json:"index"`
					Name               string  `json:"name"`
					TotalMemoryMB      int     `json:"total_memory_mb"`
					FreeMemoryMB       int     `json:"free_memory_mb"`
					UsedMemoryMB       int     `json:"used_memory_mb"`
					UtilizationPercent *int    `json:"utilization_percent"`
					TemperatureC       *int    `json:"temperature_c"`
					IsAvailable        bool    `json:"is_available"`
					ErrorMessage       *string `json:"error_message"`
				} `json:"devices"`
				Count int `json:"count"`
			}
			json.NewDecoder(gpuResp.Body).Decode(&gpuData)
			gpuResp.Body.Close()

			for _, dev := range gpuData.Devices {
				gpuDevices = append(gpuDevices, GPUDeviceInfo{
					Index:         dev.Index,
					Name:          dev.Name,
					TotalMemoryGB: float64(dev.TotalMemoryMB) / 1024,
					FreeMemoryGB:  float64(dev.FreeMemoryMB) / 1024,
					UsedMemoryGB:  float64(dev.UsedMemoryMB) / 1024,
					Utilization:   dev.UtilizationPercent,
					Temperature:   dev.TemperatureC,
					Available:     dev.IsAvailable,
					Error:         dev.ErrorMessage,
				})
			}
		}

		// Extract model names from Models map
		models := []string{}
		for modelId := range node.Models {
			models = append(models, modelId)
		}

		checkID := fmt.Sprintf("mlnode_%s", node.Id)
		status := PASS
		message := "MLNode is healthy"
		if !healthy {
			status = FAIL
			message = fmt.Sprintf("MLNode health check failed: %s", errorMsg)
		}

		checks = append(checks, Check{
			ID:      checkID,
			Status:  status,
			Message: message,
			Details: map[string]interface{}{
				"id":     node.Id,
				"host":   node.Host,
				"models": models,
				"gpus":   gpuDevices,
			},
		})
	}

	return checks
}

func (s *Server) checkBlockSync(ctx context.Context) Check {
	chainNodeUrl := s.configManager.GetChainNodeConfig().Url
	rpcClient, err := cosmosclient.NewRpcClient(chainNodeUrl)
	if err != nil {
		return Check{
			ID:      "block_sync",
			Status:  UNAVAILABLE,
			Message: fmt.Sprintf("Failed to connect to chain node: %s", err.Error()),
		}
	}

	status, err := rpcClient.Status(context.Background())
	if err != nil {
		return Check{
			ID:      "block_sync",
			Status:  UNAVAILABLE,
			Message: fmt.Sprintf("Failed to get node status: %s", err.Error()),
		}
	}

	latestBlockTime := status.SyncInfo.LatestBlockTime
	latestBlockHeight := status.SyncInfo.LatestBlockHeight
	timeSinceLastBlock := time.Since(latestBlockTime)

	// Check if block is recent (< 60 seconds old)
	isHealthy := timeSinceLastBlock < 60*time.Second

	checkStatus := PASS
	message := fmt.Sprintf("Chain is synced. Latest block: %d, age: %s",
		latestBlockHeight, timeSinceLastBlock.Round(time.Second))

	if !isHealthy {
		checkStatus = FAIL
		message = fmt.Sprintf("Chain appears stalled. Latest block: %d, age: %s (> 60s)",
			latestBlockHeight, timeSinceLastBlock.Round(time.Second))
	}

	return Check{
		ID:      "block_sync",
		Status:  checkStatus,
		Message: message,
		Details: map[string]interface{}{
			"latest_height":       latestBlockHeight,
			"latest_block_time":   latestBlockTime,
			"seconds_since_block": int(timeSinceLastBlock.Seconds()),
		},
	}
}

// Summary Generation

func (s *Server) generateSummary(report *SetupReport) {
	totalChecks := 0
	passedChecks := 0
	failedChecks := 0
	unavailableChecks := 0
	issues := []string{}
	recommendations := []string{}

	// Recommendation map for common check IDs
	recommendationMap := buildRecommendationMap()

	// Process all checks
	for _, check := range report.Checks {
		totalChecks++

		switch check.Status {
		case PASS:
			passedChecks++
		case FAIL:
			failedChecks++
			issues = append(issues, check.Message)
			if rec, ok := recommendationMap[check.ID]; ok {
				recommendations = append(recommendations, rec)
			}
			// Add specific recommendations for MLNode failures
			if strings.HasPrefix(check.ID, "mlnode_") {
				if details, ok := check.Details.(map[string]interface{}); ok {
					nodeID := details["id"]
					host := details["host"]
					recommendations = append(recommendations,
						fmt.Sprintf("Check MLNode '%s' is running and accessible at %s", nodeID, host))
				}
			}
		case UNAVAILABLE:
			unavailableChecks++
			issues = append(issues, fmt.Sprintf("Check unavailable: %s", check.Message))
		}

		// Check for unavailable GPUs in MLNode details
		if strings.HasPrefix(check.ID, "mlnode_") && check.Status == PASS {
			if details, ok := check.Details.(map[string]interface{}); ok {
				if gpus, ok := details["gpus"].([]GPUDeviceInfo); ok {
					if len(gpus) == 0 {
						nodeID := details["id"]
						issues = append(issues, fmt.Sprintf("No GPUs detected on MLNode '%s'", nodeID))
						recommendations = append(recommendations,
							fmt.Sprintf("Verify GPU drivers and CUDA installation on node '%s'", nodeID))
					} else {
						for _, gpu := range gpus {
							if !gpu.Available {
								nodeID := details["id"]
								issues = append(issues,
									fmt.Sprintf("GPU %d (%s) on node '%s' is not available", gpu.Index, gpu.Name, nodeID))
							}
						}
					}
				}
			}
		}
	}

	// Set overall status
	if failedChecks > 0 {
		report.OverallStatus = FAIL
	} else if unavailableChecks > 0 {
		report.OverallStatus = UNAVAILABLE
	} else {
		report.OverallStatus = PASS
	}

	report.Summary = ReportSummary{
		TotalChecks:       totalChecks,
		PassedChecks:      passedChecks,
		FailedChecks:      failedChecks,
		UnavailableChecks: unavailableChecks,
		Issues:            issues,
		Recommendations:   recommendations,
	}
}

func buildRecommendationMap() map[string]string {
	return map[string]string{
		"cold_key_configured":       "Verify ACCOUNT_PUBKEY environment variable is set correctly",
		"cold_key_not_in_keyring":   "Remove cold key from keyring - it should only be in environment config",
		"warm_key_in_keyring":       "Ensure warm key exists in keyring at configured location",
		"warm_key_address_match":    "Check KEY_NAME environment variable matches keyring key name",
		"permissions_granted":       "Run authz grant commands. See inference-chain/x/inference/permissions.go",
		"consensus_key_match":       "Verify validator node is running and consensus key matches participant registration",
		"active_in_epoch":           "Check PoC participation and ensure node is properly registered",
		"validator_not_jailed":      "Unjail validator or investigate validator status issues",
		"missed_requests_threshold": "Investigate why requests are being missed. Check MLNode health and network connectivity",
		"block_sync":                "Check chain node is running and syncing properly",
	}
}

// Helper Functions (copied from modelmanager for isolation)

func getPoCUrlWithVersion(node apiconfig.InferenceNodeConfig, version string) string {
	if version == "" {
		return getPoCUrl(node)
	}
	return getPoCUrlVersioned(node, version)
}

func getPoCUrl(node apiconfig.InferenceNodeConfig) string {
	if node.BaseURL != "" {
		return formatBaseURL(node.BaseURL, node.PoCSegment)
	}
	return formatURL(node.Host, node.PoCPort, node.PoCSegment)
}

func getPoCUrlVersioned(node apiconfig.InferenceNodeConfig, version string) string {
	if node.BaseURL != "" {
		return formatBaseURLWithVersion(node.BaseURL, version, node.PoCSegment)
	}
	return formatURLWithVersion(node.Host, node.PoCPort, version, node.PoCSegment)
}

func formatURL(host string, port int, segment string) string {
	return fmt.Sprintf("http://%s:%d%s", host, port, segment)
}

func formatURLWithVersion(host string, port int, version string, segment string) string {
	return fmt.Sprintf("http://%s:%d/%s%s", host, port, version, segment)
}

func formatBaseURL(baseURL string, segment string) string {
	// seg := segment
	// if seg == "" {
	// 	seg = "/"
	// }
	base := strings.TrimRight(baseURL, "/")
	return fmt.Sprintf("%s%s", base, segment)
}

func formatBaseURLWithVersion(baseURL string, version string, segment string) string {
	// seg := segment
	// if seg == "" {
	// 	seg = "/"
	// }
	base := strings.TrimRight(baseURL, "/")
	return fmt.Sprintf("%s/%s%s", base, version, segment)
}
