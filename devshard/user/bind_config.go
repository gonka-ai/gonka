package user

import (
	"fmt"

	"devshard/bridge"
	"devshard/types"
)

func sessionConfigAtBind(groupSize int, escrow *bridge.EscrowInfo, b bridge.MainnetBridge) (types.SessionConfig, error) {
	config := bridge.SessionConfigAtBind(groupSize, escrow)
	sb, ok := b.(bridge.SessionBindParamsBridge)
	if !ok {
		return types.NormalizeSessionConfig(config, groupSize), nil
	}
	live, err := sb.GetSessionBindParams()
	if err != nil {
		return types.SessionConfig{}, fmt.Errorf("query session bind params: %w", err)
	}
	return types.ApplyChainSessionBindParams(config, groupSize, live), nil
}
