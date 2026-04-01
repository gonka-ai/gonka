package broker

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var ErrLockNotFound = errors.New("lock not found")

// AcquireMLNode queues a LockAvailableNode command, waits for a node,
// records it in the lock map, and returns (lockID, inferenceURL, nodeID).
func (b *Broker) AcquireMLNode(ctx context.Context, model string, skipNodeIDs []string) (lockID, endpoint, nodeID string, err error) {
	ch := make(chan *Node, 2)
	err = b.QueueMessage(LockAvailableNode{
		Model:       model,
		SkipNodeIDs: skipNodeIDs,
		Response:    ch,
	})
	if err != nil {
		return
	}

	select {
	case <-ctx.Done():
		return "", "", "", ctx.Err()
	case node := <-ch:
		if node == nil {
			return "", "", "", ErrNoNodesAvailable
		}
		lockID := uuid.New().String()
		b.lockMapMu.Lock()
		b.lockMap[lockID] = lockEntry{nodeID: node.Id, createdAt: time.Now()}
		b.lockMapMu.Unlock()
		version := b.configManager.GetCurrentNodeVersion()
		return lockID, node.InferenceUrlWithVersion(version), node.Id, nil
	}
}

// ReleaseMLNode removes the lock from the map and queues a ReleaseNode command.
func (b *Broker) ReleaseMLNode(lockID string, outcome InferenceResult) error {
	b.lockMapMu.Lock()
	entry, ok := b.lockMap[lockID]
	delete(b.lockMap, lockID)
	b.lockMapMu.Unlock()

	if !ok {
		return ErrLockNotFound
	}

	_ = b.QueueMessage(ReleaseNode{
		NodeId:   entry.nodeID,
		Outcome:  outcome,
		Response: make(chan bool, 1),
	})
	return nil
}

// evictExpiredLocks is called from reconcilerLoop to release locks held longer than the TTL.
// It collects expired entries without holding lockMapMu while calling QueueMessage,
// to avoid a deadlock if the queue is momentarily full.
func (b *Broker) evictExpiredLocks() {
	ttlSeconds := b.configManager.GetApiConfig().NodeManagerLockTTLSeconds
	ttl := time.Duration(ttlSeconds) * time.Second

	now := time.Now()
	b.lockMapMu.Lock()
	var expired []lockEntry
	for lockID, entry := range b.lockMap {
		if now.Sub(entry.createdAt) > ttl {
			expired = append(expired, entry)
			delete(b.lockMap, lockID)
		}
	}
	b.lockMapMu.Unlock()

	for _, entry := range expired {
		_ = b.QueueMessage(ReleaseNode{
			NodeId:   entry.nodeID,
			Outcome:  InferenceError{Message: "lock TTL expired"},
			Response: make(chan bool, 2),
		})
	}
}
