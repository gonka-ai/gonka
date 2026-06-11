package apiconfig

import (
	"github.com/productscience/inference/x/inference/types"
)

// DevshardVersionsCacheFromParams maps chain DevshardEscrowParams into the dapi cache.
func DevshardVersionsCacheFromParams(dep *types.DevshardEscrowParams) DevshardVersionsCache {
	if dep == nil {
		return DevshardVersionsCache{}
	}
	versions := make([]DevshardVersion, len(dep.ApprovedVersions))
	for i, v := range dep.ApprovedVersions {
		if v == nil {
			continue
		}
		versions[i] = DevshardVersion{
			Name: v.Name, Binary: v.Binary, SHA256: v.Sha256,
		}
	}
	return DevshardVersionsCache{
		Versions:                versions,
		DevshardRequestsEnabled: dep.DevshardRequestsEnabled,
		MaxNonce:                dep.MaxNonce,
		RefusalTimeout:          dep.RefusalTimeout,
		ExecutionTimeout:        dep.ExecutionTimeout,
		ValidationRate:          dep.ValidationRate,
		VoteThresholdFactor:     dep.VoteThresholdFactor,
	}
}
