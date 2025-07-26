package upgrade

import (
	"decentralized-api/apiconfig"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/event_listener/chainevents"
	"decentralized-api/logging"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/productscience/inference/x/inference/types"
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

	checkForPartialUpgradesScheduled(transactionRecorder, configManager)
	checkForFullUpgradesScheduled(transactionRecorder, configManager)

	checkForVersionSwitch(configManager)
}

func checkForPartialUpgradesScheduled(transactionRecorder cosmosclient.InferenceCosmosClient, configManager *apiconfig.ConfigManager) {
	partialUpgrades, err := transactionRecorder.GetPartialUpgrades()
	if err != nil {
		logging.Error("Error getting partial upgrades", types.Upgrades, "error", err)
		return
	}
	logging.Info("checkForPartialUpgrades. Partial upgrades", types.Upgrades, "partialUpgrades", partialUpgrades)
	for _, upgrade := range partialUpgrades.PartialUpgrade {
		// If Binaries are provided, we set everything
		if upgrade.ApiBinariesJson != "" {
			var planInfo UpgradeInfoInput
			if err := json.Unmarshal([]byte(upgrade.ApiBinariesJson), &planInfo); err != nil {
				logging.Error("Error unmarshalling upgrade plan info", types.Upgrades, "error", err)
				continue
			}
			if planInfo.Binaries == nil || len(planInfo.Binaries) == 0 {
				continue
			}
			if upgrade.Name == configManager.GetUpgradePlan().Name {
				logging.Info("Upgrade already ready", types.Upgrades, "name", upgrade.Name)
				continue
			}
			err = configManager.SetUpgradePlan(apiconfig.UpgradePlan{
				Name:        upgrade.Name,
				Height:      int64(upgrade.Height),
				Binaries:    planInfo.Binaries,
				NodeVersion: planInfo.NodeVersion, // Store the known version
			})
			if err != nil {
				logging.Error("Error setting upgrade with binaries", types.Upgrades, "error", err)
				continue
			}
			continue
		}
		// If Binaries are not provided but NodeVersion is, we set the NodeVersion, ignoring conflicts
		if upgrade.NodeVersion != "" {
			err = configManager.SetUpgradePlan(apiconfig.UpgradePlan{
				Name:        upgrade.Name,
				Height:      int64(upgrade.Height),
				NodeVersion: upgrade.NodeVersion,
			})
			if err != nil {
				logging.Error("Error setting upgrade plan for node version", types.Upgrades, "error", err)
				continue
			}
			continue
		}
	}
}

func checkForFullUpgradesScheduled(transactionRecorder cosmosclient.InferenceCosmosClient, configManager *apiconfig.ConfigManager) {
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
			Name:        upgradePlan.Plan.Name,
			Height:      upgradePlan.Plan.Height,
			Binaries:    planInfo.Binaries,
			NodeVersion: planInfo.NodeVersion,
		})
		if err != nil {
			logging.Error("Error setting upgrade plan", types.Upgrades, "error", err)
			return
		}
	}
}

func checkForVersionSwitch(configManager *apiconfig.ConfigManager) {
	upgradePlan := configManager.GetUpgradePlan()

	if upgradePlan.Name == "" {
		logging.Warn("checkForVersionSwitch. Websocket closed with no upgrade (name is empty)", types.Upgrades)
		return
	}

	if configManager.GetHeight() >= upgradePlan.Height-1 {
		if upgradePlan.NodeVersion != "" {
			oldVersion := configManager.GetCurrentNodeVersion()
			if upgradePlan.NodeVersion != oldVersion {
				err := configManager.SetCurrentNodeVersion(upgradePlan.NodeVersion)
				if err != nil {
					logging.Error("checkForVersionSwitch. Failed to update MLNode version in config", types.Upgrades, "error", err)
					return
				}
				logging.Info("MLNode version updated during upgrade using known target version", types.Upgrades, "oldVersion", oldVersion, "newVersion", upgradePlan.NodeVersion)
				if len(upgradePlan.Binaries) == 0 {
					configManager.ClearUpgradePlan()
				}
			}
		} else {
			logging.Warn("No NodeVersion specified in upgrade plan", types.Upgrades, "upgradeName", upgradePlan.Name)
		}
	}
}

func CheckForUpgrade(configManager *apiconfig.ConfigManager) bool {
	upgradePlan := configManager.GetUpgradePlan()
	if upgradePlan.Name == "" {
		logging.Warn("CheckForUpgrade. Websocket closed with no upgrade (name is empty)", types.Upgrades)
		return false
	}

	successfullyUpgraded := false
	if configManager.GetHeight() >= upgradePlan.Height-1 {
		logging.Info("CheckForUpgrade. Upgrade height reached", types.Upgrades, "height", upgradePlan.Height)

		checkForVersionSwitch(configManager)
		if len(upgradePlan.Binaries) == 0 {
			return successfullyUpgraded
		}

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
		logging.Info("Upgrade output written to file, clearing upgrade plan", types.Upgrades, "path", path)
		configManager.ClearUpgradePlan()
		return true
	}

	logging.Warn("Websocket closed with no upgrade", types.Upgrades, "height", configManager.GetHeight(), "upgradeHeight", upgradePlan.Height)
	return false
}

func getUpgradeInfoPath() string {
	return "../data/upgrade-info.json"
}
