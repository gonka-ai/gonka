package upgrade

import (
	"decentralized-api/apiconfig"
	"decentralized-api/chainevents"
	"decentralized-api/cosmosclient"
	"encoding/json"
	"github.com/sagikazarmark/slog-shim"
	"os"
	"path/filepath"
)

func ProcessNewBlockEvent(
	event *chainevents.JSONRPCResponse,
	transactionRecorder cosmosclient.InferenceCosmosClient,
	configManager *apiconfig.ConfigManager,
) {
	if event.Result.Data.Type != "tendermint/event/NewBlock" {
		slog.Error("Expected tendermint/event/NewBlock event", "event", event.Result.Data.Type)
		return
	}

	// Check for any upcoming upgrade plan
	upgradePlan, err := transactionRecorder.GetUpgradePlan()
	if err != nil {
		slog.Error("Error getting upgrade plan", "error", err)
		return
	}

	if upgradePlan != nil && upgradePlan.Plan != nil {
		if upgradePlan.Plan.Name == configManager.GetUpgradePlan().Name {
			slog.Info("Upgrade already ready", "name", upgradePlan.Plan.Name)
			return
		}
		if upgradePlan.Plan.Info == "" {
			slog.Error("Upgrade exists, no info for api binaries")
			return
		}
		var planInfo UpgradeInfoInput
		if err := json.Unmarshal([]byte(upgradePlan.Plan.Info), &planInfo); err != nil {
			slog.Error("Error unmarshalling upgrade plan info", "error", err)
			return
		}
		err = configManager.SetUpgradePlan(apiconfig.UpgradePlan{
			Name:     upgradePlan.Plan.Name,
			Height:   upgradePlan.Plan.Height,
			Binaries: planInfo.Binaries,
		})
		if err != nil {
			slog.Error("Error setting upgrade plan", "error", err)
			return
		}
	}

}

func CheckForUpgrade(configManager *apiconfig.ConfigManager) bool {
	upgradePlan := configManager.GetUpgradePlan()
	if upgradePlan.Name == "" {
		slog.Warn("Websocket closed with no upgrade")
		return false
	}

	if configManager.GetHeight() >= upgradePlan.Height-1 {
		slog.Info("Upgrade height reached", "height", upgradePlan.Height)
		// Upgrade
		// Write out upgrade-info.json
		path := getUpgradeInfoPath()
		upgradeInfo := UpgradeInfoOutput{
			Binaries: upgradePlan.Binaries,
		}

		jsonData, err := json.Marshal(upgradeInfo)
		if err != nil {
			slog.Error("Error marshaling upgrade info to JSON", "error", err)
			return false
		}
		output := UpgradeOutput{
			Name: upgradePlan.Name,
			// We add one, because the chain quits ON the upgrade height before it sends the new block event
			Height: upgradePlan.Height - 1,
			Info:   string(jsonData),
		}
		jsonData, err = json.Marshal(output)
		if err != nil {
			slog.Error("Error marshaling output to JSON", "error", err)
			return false
		}

		err = os.MkdirAll(filepath.Dir(path), os.ModePerm)
		if err != nil {
			slog.Error("Error creating output directory", "path", path, "error", err)
			return false
		}

		err = os.WriteFile(path, jsonData, 0644)
		if err != nil {
			slog.Error("Error writing output to file", "path", path, "error", err)
			return false
		}
		slog.Info("Upgrade output written to file", "path", path)
		return true
	}

	slog.Warn("Websocket closed with no upgrade", "height", configManager.GetHeight(), "upgradeHeight", upgradePlan.Height)
	return false
}

func getUpgradeInfoPath() string {
	return "../data/upgrade-info.json"
}
