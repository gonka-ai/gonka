package broker

import (
	"common/logging"
	"context"
	"decentralized-api/mlnodeclient"
	"sync"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

const defaultWorkerShutdownTimeout = 5 * time.Second

type commandWithContext struct {
	cmd        NodeWorkerCommand
	ctx        context.Context
	generation uint64
}

// NodeWorker handles asynchronous operations for a specific node
type NodeWorker struct {
	nodeId            string
	node              *NodeWithState
	getClientFn       func(*NodeWithState) mlnodeclient.MLNodeClient
	broker            *Broker
	commands          chan commandWithContext
	shutdown          chan struct{}
	closed            bool
	closedMu          sync.Mutex
	wg                sync.WaitGroup
	availableVersions map[string]bool
	versionsMu        sync.Mutex
}

// NewNodeWorker creates a worker that builds a new client for every operation using the latest node state.
func NewNodeWorker(nodeId string, node *NodeWithState, broker *Broker) *NodeWorker {
	return newNodeWorker(nodeId, node, broker, nil)
}

// NewNodeWorkerWithClient creates a new worker with a custom client provider (primarily for testing).
func NewNodeWorkerWithClient(nodeId string, node *NodeWithState, client mlnodeclient.MLNodeClient, broker *Broker) *NodeWorker {
	return newNodeWorker(nodeId, node, broker, func(*NodeWithState) mlnodeclient.MLNodeClient {
		return client
	})
}

func newNodeWorker(nodeId string, node *NodeWithState, broker *Broker, getClientFn func(*NodeWithState) mlnodeclient.MLNodeClient) *NodeWorker {
	worker := &NodeWorker{
		nodeId:            nodeId,
		node:              node,
		broker:            broker,
		commands:          make(chan commandWithContext, 10),
		availableVersions: make(map[string]bool),
		shutdown:          make(chan struct{}),
	}
	if getClientFn != nil {
		worker.getClientFn = getClientFn
	} else {
		worker.getClientFn = func(nodeState *NodeWithState) mlnodeclient.MLNodeClient {
			return broker.NewNodeClient(&nodeState.Node)
		}
	}
	go worker.run()
	return worker
}

// run is the main event loop for the worker
func (w *NodeWorker) run() {
	for {
		select {
		case item := <-w.commands:
			result := item.cmd.Execute(item.ctx, w)
			result.DeploymentGeneration = item.generation

			// Queue a command back to the broker to update the state
			updateCmd := NewUpdateNodeResultCommand(w.nodeId, result)
			if err := w.broker.QueueMessage(updateCmd); err != nil {
				logging.Error("Failed to queue node result update command", types.Nodes,
					"node_id", w.nodeId, "error", err)
			}
			// We don't wait for the response from updateCmd, the worker's job is done.
			w.wg.Done()
		case <-w.shutdown:
			// Drain remaining commands before shutting down
			close(w.commands)
			for item := range w.commands {
				result := item.cmd.Execute(item.ctx, w)
				result.DeploymentGeneration = item.generation
				updateCmd := NewUpdateNodeResultCommand(w.nodeId, result)
				if err := w.broker.QueueMessage(updateCmd); err != nil {
					logging.Error("Failed to queue node result update command during shutdown", types.Nodes,
						"node_id", w.nodeId, "error", err)
				}
				w.wg.Done()
			}
			return
		}
	}
}

// Submit queues a command for execution on this node.
// Returns false if the worker is shut down or the command queue is full.
func (w *NodeWorker) Submit(ctx context.Context, cmd NodeWorkerCommand) bool {
	return w.submit(ctx, cmd, 0)
}

func (w *NodeWorker) submit(ctx context.Context, cmd NodeWorkerCommand, generation uint64) bool {
	w.closedMu.Lock()
	defer w.closedMu.Unlock()
	if w.closed {
		return false
	}
	w.wg.Add(1)
	select {
	case w.commands <- commandWithContext{cmd: cmd, ctx: ctx, generation: generation}:
		return true
	default:
		w.wg.Done()
		return false
	}
}

// Shutdown stops the worker, waiting up to defaultWorkerShutdownTimeout for
// in-flight commands. Callers that need a different bound should use
// ShutdownWithTimeout.
func (w *NodeWorker) Shutdown() {
	_ = w.ShutdownWithTimeout(defaultWorkerShutdownTimeout)
}

// ShutdownWithTimeout signals the worker to stop and waits up to d for in-flight
// commands to finish. It returns true if the worker drained cleanly. Safe to call
// more than once: only the first call closes the shutdown channel.
func (w *NodeWorker) ShutdownWithTimeout(d time.Duration) bool {
	w.closedMu.Lock()
	alreadyClosed := w.closed
	if !w.closed {
		w.closed = true
		close(w.shutdown)
	}
	w.closedMu.Unlock()

	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()

	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		if !alreadyClosed {
			logging.Error("Node worker shutdown timed out; abandoning in-flight command", types.Nodes,
				"node_id", w.nodeId,
				"timeout", d,
				"likely_cause", "in-flight ML call or full broker queue")
		}
		return false
	}
}

func (w *NodeWorker) RefreshClientImmediate(oldVersion, newVersion string) {
	logging.Info("Node worker uses dynamic MLNode clients per request; future calls will use latest version info", types.Nodes,
		"node_id", w.nodeId, "oldVersion", oldVersion, "newVersion", newVersion)
}

// isVersionKnownAlive checks the cache to see if a version is already known to be alive.
// It returns true if it's cached and alive, false otherwise.
func (w *NodeWorker) isVersionKnownAlive(version string) bool {
	w.versionsMu.Lock()
	defer w.versionsMu.Unlock()
	if alive, ok := w.availableVersions[version]; ok && alive {
		return true
	}
	return false
}

func (w *NodeWorker) CheckClientVersionAlive(version string, factory mlnodeclient.ClientFactory) (bool, error) {
	if w.isVersionKnownAlive(version) {
		return true, nil
	}

	node := w.node.Node
	pocUrl := node.PoCUrlWithVersion(version)
	inferenceUrl := node.InferenceUrlWithVersion(version)

	versionClient := factory.CreateClient(pocUrl, inferenceUrl)
	_, err := versionClient.NodeState(context.Background())

	w.versionsMu.Lock()
	defer w.versionsMu.Unlock()
	if err != nil {
		w.availableVersions[version] = false
		return false, err
	}
	w.availableVersions[version] = true
	return true, nil
}

func (w *NodeWorker) GetClient() mlnodeclient.MLNodeClient {
	return w.getClientFn(w.node)
}

// NodeWorkGroup manages parallel execution across multiple node workers
type NodeWorkGroup struct {
	workers map[string]*NodeWorker
	mu      sync.RWMutex
}

// NewNodeWorkGroup creates a new work group
func NewNodeWorkGroup() *NodeWorkGroup {
	return &NodeWorkGroup{
		workers: make(map[string]*NodeWorker),
	}
}

// AddWorker adds a new worker to the group
func (g *NodeWorkGroup) AddWorker(nodeId string, worker *NodeWorker) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.workers[nodeId] = worker
}

// RemoveWorker unregisters the worker and waits for it to shut down.
func (g *NodeWorkGroup) RemoveWorker(nodeId string) {
	if worker := g.unregisterWorker(nodeId); worker != nil {
		worker.Shutdown()
	}
}

// RemoveWorkerAsync unregisters the worker immediately and shuts it down in the
// background so the caller (the broker command loop) is not blocked.
func (g *NodeWorkGroup) RemoveWorkerAsync(nodeId string) {
	if worker := g.unregisterWorker(nodeId); worker != nil {
		go worker.ShutdownWithTimeout(defaultWorkerShutdownTimeout)
	}
}

func (g *NodeWorkGroup) unregisterWorker(nodeId string) *NodeWorker {
	g.mu.Lock()
	defer g.mu.Unlock()
	worker, exists := g.workers[nodeId]
	if !exists {
		return nil
	}
	delete(g.workers, nodeId)
	return worker
}

// GetWorker returns a specific worker (useful for node-specific commands)
func (g *NodeWorkGroup) GetWorker(nodeId string) (*NodeWorker, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	worker, exists := g.workers[nodeId]
	return worker, exists
}
