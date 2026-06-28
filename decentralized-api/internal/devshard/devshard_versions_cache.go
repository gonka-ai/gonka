package devshard

import (
	"decentralized-api/apiconfig"

	"github.com/productscience/inference/x/inference/types"
)

// SeedDevshardVersionsCache copies chain DevshardEscrowParams into the dapi cache
// so embedded host availability and runtime snapshots are correct before the
// first block is processed by the event listener.
func SeedDevshardVersionsCache(cm *apiconfig.ConfigManager, dep *types.DevshardEscrowParams) {
	if cm == nil || dep == nil {
		return
	}
	cm.SetDevshardVersions(apiconfig.DevshardVersionsCacheFromParams(dep))
}
