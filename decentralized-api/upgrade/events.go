package upgrade

import (
	"decentralized-api/apiconfig"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/event_listener/chainevents"
	"decentralized-api/logging"
	"encoding/json"
	"github.com/productscience/inference/x/inference/types"
	"os"
	"path/filepath"
)

func ProcessNewBlockEvent(
	event *chainevents.JSONRPCResponse,
	transactionRecorder cosmosclient.InferenceCosmosClient,
	configManager *apiconfig.ConfigManager,
) {
	if event.Result.Data.Type != "tendermint/event/NewBlock" {
		logging.Error("Expected tendermint/event/NewBlock event", types.Upgrades, "event", event.Result.Data.Type)
		return
	}

	// Check for any upcoming upgrade plan
	upgradePlan, err := transactionRecorder.GetUpgradePlan()
	if err != nil {
		logging.Error("Error getting upgrade plan", types.Upgrades, "error", err)
		return
	}

	if upgradePlan != nil && upgradePlan.Plan != nil {
		if upgradePlan.Plan.Name == configManager.GetUpgradePlan().Name {
			logging.Info("Upgrade already ready", types.Upgrades, "name", upgradePlan.Plan.Name)
			return
		}
		if upgradePlan.Plan.Info == "" {
			logging.Error("Upgrade exists, no info for api binaries", types.Upgrades)
			return
		}
		var planInfo UpgradeInfoInput
		if err := json.Unmarshal([]byte(upgradePlan.Plan.Info), &planInfo); err != nil {
			logging.Error("Error unmarshalling upgrade plan info", types.Upgrades, "error", err)
			return
		}
		err = configManager.SetUpgradePlan(apiconfig.UpgradePlan{
			Name:     upgradePlan.Plan.Name,
			Height:   upgradePlan.Plan.Height,
			Binaries: planInfo.Binaries,
		})
		if err != nil {
			logging.Error("Error setting upgrade plan", types.Upgrades, "error", err)
			return
		}
	}

}

func CheckForUpgrade(configManager *apiconfig.ConfigManager) bool {
	upgradePlan := configManager.GetUpgradePlan()
	if upgradePlan.Name == "" {
		logging.Warn("Websocket closed with no upgrade", types.Upgrades)
		return false
	}

	if configManager.GetHeight() >= upgradePlan.Height-1 {
		logging.Info("Upgrade height reached", types.Upgrades, "height", upgradePlan.Height)
		// Upgrade
		// Write out upgrade-info.json
		path := getUpgradeInfoPath()
		upgradeInfo := UpgradeInfoOutput{
			Binaries: upgradePlan.Binaries,
		}

		jsonData, err := json.Marshal(upgradeInfo)
		if err != nil {
			logging.Error("Error marshaling upgrade info to JSON", types.Upgrades, "error", err)
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
			logging.Error("Error marshaling output to JSON", types.Upgrades, "error", err)
			return false
		}

		err = os.MkdirAll(filepath.Dir(path), os.ModePerm)
		if err != nil {
			logging.Error("Error creating output directory", types.Upgrades, "path", path, "error", err)
			return false
		}

		err = os.WriteFile(path, jsonData, 0644)
		if err != nil {
			logging.Error("Error writing output to file", types.Upgrades, "path", path, "error", err)
			return false
		}
		logging.Info("Upgrade output written to file", types.Upgrades, "path", path)
		return true
	}

	logging.Warn("Websocket closed with no upgrade", types.Upgrades, "height", configManager.GetHeight(), "upgradeHeight", upgradePlan.Height)
	return false
}

func getUpgradeInfoPath() string {
	return "../data/upgrade-info.json"
}
