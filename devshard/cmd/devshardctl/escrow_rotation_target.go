package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

type keyedMutex struct {
	guard sync.Mutex
	locks map[string]*sync.Mutex
}

func (keyed *keyedMutex) lock(key string) func() {
	keyed.guard.Lock()
	if keyed.locks == nil {
		keyed.locks = make(map[string]*sync.Mutex)
	}
	keyLock, ok := keyed.locks[key]
	if !ok {
		keyLock = &sync.Mutex{}
		keyed.locks[key] = keyLock
	}
	keyed.guard.Unlock()
	keyLock.Lock()
	return keyLock.Unlock
}

func rotationTargetKey(modelID, role string, epoch uint64) string {
	return modelID + "|" + role + "|" + strconv.FormatUint(epoch, 10)
}

func countActiveRotationEscrows(devshards []GatewayDevshardState, role string, epoch uint64, modelID string) int {
	count := 0
	for _, devshard := range devshards {
		if devshard.RotationRole == role && devshard.RotationEpoch == epoch && devshard.Active && devshard.OnHoldSince == "" && strings.TrimSpace(devshard.Model) == modelID {
			count++
		}
	}
	return count
}

func (g *Gateway) activeRotationEscrowCount(ctx context.Context, role string, epoch uint64, modelID string) (int, error) {
	state, ok, err := g.store.LoadState(ctx)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("gateway state is not initialized")
	}
	serving := make([]GatewayDevshardState, 0, len(state.Devshards))
	for _, devshard := range state.Devshards {
		if !g.isAwaitingStart(devshard.ID) {
			serving = append(serving, devshard)
		}
	}
	return countActiveRotationEscrows(serving, role, epoch, modelID), nil
}
