package upgrade

import (
	"common/logging"
	"decentralized-api/apiconfig"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/event_listener/chainevents"
	"encoding/json"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/productscience/inference/x/inference/types"
)

var (
	pendingUpgradeMu sync.Mutex
	pendingUpgrade   *apiconfig.UpgradePlan
	upgradeInfoPath  = "../data/upgrade-info.json"
	upgradeWaitDone  chan struct{}
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

	if upgradePlan.Name == "" || upgradePlan.NodeVersion == "" {
		logging.Debug("checkForVersionSwitch. name or node version is empty", types.Upgrades, "upgradePlan", upgradePlan)
		return
	}

	if configManager.GetHeight() >= upgradePlan.Height-1 {
		logging.Info("checkForVersionSwitch. height reached for upgrade", types.Upgrades, "nodeVersion", upgradePlan.NodeVersion, "upgradeHeight", upgradePlan.Height)
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
	}
}

func CheckForUpgrade(configManager *apiconfig.ConfigManager) bool {
	upgradePlan := configManager.GetUpgradePlan()
	if upgradePlan.Name == "" {
		logging.Warn("CheckForUpgrade. Websocket closed with no upgrade (name is empty)", types.Upgrades)
		return false
	}

	if configManager.GetHeight() >= upgradePlan.Height-1 {
		logging.Info("CheckForUpgrade. Upgrade height reached", types.Upgrades, "height", upgradePlan.Height)

		checkForVersionSwitch(configManager)
		if len(upgradePlan.Binaries) == 0 {
			return false
		}

		// Publish the file before clearing the plan. A failed write must leave
		// the plan in place and must not shut the process down. The exit path
		// waits after this returns so it does not race cosmovisor's poller.
		path := getUpgradeInfoPath()
		if err := writeUpgradeInfo(path, upgradePlan); err != nil {
			logging.Error("Error writing upgrade info", types.Upgrades, "path", path, "error", err)
			return false
		}
		logging.Info("Upgrade output written to file, clearing upgrade plan", types.Upgrades, "path", path)
		stagePendingUpgrade(upgradePlan)
		if err := configManager.ClearUpgradePlan(); err != nil {
			logging.Error("Error clearing upgrade plan", types.Upgrades, "error", err)
		}
		return true
	}

	logging.Warn("Websocket closed with no upgrade", types.Upgrades, "height", configManager.GetHeight(), "upgradeHeight", upgradePlan.Height)
	return false
}

// FinishUpgradeForCosmovisor blocks until cosmovisor terminates this process
// (SIGKILL by default, or SIGTERM when DAEMON_SHUTDOWN_GRACE is configured).
// CheckForUpgrade has already published upgrade-info.json. Exiting on our own
// triggers WaitForUpgradeOrExit's CheckUpdate in parallel with MonitorUpdate.
func FinishUpgradeForCosmovisor() {
	if _, ok := consumePendingUpgrade(); !ok {
		return
	}
	logging.Info("Waiting for cosmovisor to relaunch", types.Upgrades)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigCh)

	pendingUpgradeMu.Lock()
	done := upgradeWaitDone
	pendingUpgradeMu.Unlock()

	select {
	case <-sigCh:
	case <-done:
	}
}

func stagePendingUpgrade(plan apiconfig.UpgradePlan) {
	pendingUpgradeMu.Lock()
	defer pendingUpgradeMu.Unlock()
	copied := plan
	pendingUpgrade = &copied
}

func consumePendingUpgrade() (apiconfig.UpgradePlan, bool) {
	pendingUpgradeMu.Lock()
	defer pendingUpgradeMu.Unlock()
	if pendingUpgrade == nil {
		return apiconfig.UpgradePlan{}, false
	}
	plan := *pendingUpgrade
	pendingUpgrade = nil
	return plan, true
}

func writeUpgradeInfo(path string, plan apiconfig.UpgradePlan) error {
	upgradeInfo := UpgradeInfoOutput{Binaries: plan.Binaries}
	infoJSON, err := json.Marshal(upgradeInfo)
	if err != nil {
		return err
	}
	output := UpgradeOutput{
		Name: plan.Name,
		// The chain halts ON the upgrade height, before the new-block event.
		Height: plan.Height - 1,
		Info:   string(infoJSON),
	}
	jsonData, err := json.Marshal(output)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, os.ModePerm); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".upgrade-info-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(jsonData); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, 0644); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

func getUpgradeInfoPath() string {
	return upgradeInfoPath
}
