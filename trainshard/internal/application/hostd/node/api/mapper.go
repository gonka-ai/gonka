package api

import (
	"trainshard/internal/contract"
	"trainshard/internal/domain/readiness"
	"trainshard/internal/domain/shared/vo"
)

func toReadinessOutput(nodes []vo.NodeRef, version string, results map[vo.NodeRef]readiness.Result) contract.ReadinessResult {
	items := make([]contract.NodeReadiness, 0, len(nodes))
	ready := len(nodes) > 0
	reason := ""

	for _, node := range nodes {
		result := results[node]
		items = append(items, contract.NodeReadiness{
			NodeID: string(node.NodeID),
			Ready:  result.Ready,
			Reason: result.Reason(),
		})
		if !result.Ready {
			ready = false
			if reason == "" {
				reason = string(node.NodeID) + ": " + result.Reason()
			}
		}
	}

	return contract.ReadinessResult{Ready: ready, Reason: reason, Version: version, Items: items}
}
