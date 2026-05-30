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
	versions := make([]apiconfig.DevshardVersion, len(dep.ApprovedVersions))
	for i, v := range dep.ApprovedVersions {
		versions[i] = apiconfig.DevshardVersion{
			Name: v.Name, Binary: v.Binary, SHA256: v.Sha256,
		}
	}
	cm.SetDevshardVersions(apiconfig.DevshardVersionsCache{
		Versions:                          versions,
		DevshardRequestsEnabled:           dep.DevshardRequestsEnabled,
		DefaultSealGraceNonces:            dep.DefaultSealGraceNonces,
		DefaultInferenceClearGraceSeconds: dep.DefaultInferenceClearGraceSeconds,
		MaxNonce:                          dep.MaxNonce,
	})
}
