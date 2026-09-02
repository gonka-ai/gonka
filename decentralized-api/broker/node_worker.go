package broker

import (
	"common/logging"
	"context"
	"decentralized-api/mlnodeclient"
	"sync"

	"github.com/productscience/inference/x/inference/types"
)

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
	shutdownOnce      sync.Once
	mu                sync.Mutex
	stopping          bool
	wg                sync.WaitGroup
	registrationSeq   uint64
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
		registrationSeq:   node.State.RegistrationSeq,
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
			if w.isStopping() {
				// select can pick a queued command even after shutdown is signaled.
				// Drop it instead of starting another ML-node HTTP call.
				w.wg.Done()
				w.logDropped(1 + w.dropQueuedCommands())
				return
			}
			w.execute(item)
		case <-w.shutdown:
			w.logDropped(w.dropQueuedCommands())
			return
		}
	}
}

func (w *NodeWorker) execute(item commandWithContext) {
	result := item.cmd.Execute(item.ctx, w)
	result.DeploymentGeneration = item.generation
	result.RegistrationSeq = w.registrationSeq

	updateCmd := NewUpdateNodeResultCommand(w.nodeId, result)
	if err := w.broker.QueueMessage(updateCmd); err != nil {
		logging.Error("Failed to queue node result update command", types.Nodes,
			"node_id", w.nodeId, "error", err)
	}
	w.wg.Done()
}

func (w *NodeWorker) isStopping() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stopping
}

func (w *NodeWorker) dropQueuedCommands() int {
	dropped := 0
	for {
		select {
		case <-w.commands:
			dropped++
			w.wg.Done()
		default:
			return dropped
		}
	}
}

func (w *NodeWorker) logDropped(dropped int) {
	if dropped > 0 {
		logging.Info("Dropped queued worker commands on shutdown", types.Nodes,
			"node_id", w.nodeId, "dropped", dropped)
	}
}

// Submit queues a command for execution on this node
func (w *NodeWorker) Submit(ctx context.Context, cmd NodeWorkerCommand) bool {
	return w.submit(ctx, cmd, 0)
}

func (w *NodeWorker) submit(ctx context.Context, cmd NodeWorkerCommand, generation uint64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopping {
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

// signalShutdown marks the worker as stopping and wakes the run loop.
// It does not wait for in-flight HTTP. Safe to call twice.
func (w *NodeWorker) signalShutdown() {
	w.shutdownOnce.Do(func() {
		w.mu.Lock()
		w.stopping = true
		w.mu.Unlock()
		close(w.shutdown)
		// Drain now. The run loop cannot do this while it is blocked in
		// Execute (up to the 15-minute ML-node timeout).
		w.logDropped(w.dropQueuedCommands())
	})
}

// Shutdown stops the worker and waits for in-flight work to finish.
// The in-flight command may still run until its context is cancelled;
// queued commands are dropped. Safe to call twice.
func (w *NodeWorker) Shutdown() {
	w.signalShutdown()
	w.wg.Wait()
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

// AddWorker registers worker only when nodeId has no worker.
// It does not shut down or replace an existing worker.
func (g *NodeWorkGroup) AddWorker(nodeId string, worker *NodeWorker) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.workers[nodeId]; exists {
		return false
	}
	g.workers[nodeId] = worker
	return true
}

// RemoveWorker unregisters the worker immediately and shuts it down in the
// background. Must not wait: callers run on the shared broker command loop.
func (g *NodeWorkGroup) RemoveWorker(nodeId string) {
	g.mu.Lock()
	worker, exists := g.workers[nodeId]
	if exists {
		delete(g.workers, nodeId)
	}
	g.mu.Unlock()

	if exists {
		// Reject submits and drain the queue before returning. Wait for
		// in-flight HTTP in the background so the broker command loop cannot stall.
		worker.signalShutdown()
		go worker.Shutdown()
	}
}

// GetWorker returns a specific worker (useful for node-specific commands)
func (g *NodeWorkGroup) GetWorker(nodeId string) (*NodeWorker, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	worker, exists := g.workers[nodeId]
	return worker, exists
}
