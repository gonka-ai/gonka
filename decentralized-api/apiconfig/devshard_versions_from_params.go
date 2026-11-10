package apiconfig

import (
	"github.com/productscience/inference/x/inference/types"
)

// DevshardVersionsCacheFromParams maps escrow scalars from params and the
// dedicated approved-versions query into the dapi cache.
func DevshardVersionsCacheFromParams(dep *types.DevshardEscrowParams, approved []*types.DevshardApprovedVersion) DevshardVersionsCache {
	if dep == nil {
		return DevshardVersionsCache{}
	}
	versions := make([]DevshardVersion, 0, len(approved))
	for _, v := range approved {
		if v == nil {
			continue
		}
		versions = append(versions, DevshardVersion{
			Name: v.Name, Binary: v.Binary, SHA256: v.Sha256,
		})
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
