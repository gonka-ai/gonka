package broker

import (
	"context"
	"decentralized-api/logging"
	"decentralized-api/mlnodeclient"
	"sync"

	"github.com/productscience/inference/x/inference/types"
)

type commandWithContext struct {
	cmd NodeWorkerCommand
	ctx context.Context
}

// NodeWorker handles asynchronous operations for a specific node
type NodeWorker struct {
	nodeId   string
	node     *NodeWithState
	mlClient mlnodeclient.MLNodeClient
	commands chan commandWithContext
	shutdown chan struct{}
	wg       sync.WaitGroup
}

// NewNodeWorkerWithClient creates a new worker with a custom client (for testing)
func NewNodeWorkerWithClient(nodeId string, node *NodeWithState, client mlnodeclient.MLNodeClient) *NodeWorker {
	worker := &NodeWorker{
		nodeId:   nodeId,
		node:     node,
		mlClient: client,
		commands: make(chan commandWithContext, 10),
		shutdown: make(chan struct{}),
	}
	go worker.run()
	return worker
}

// run is the main event loop for the worker
func (w *NodeWorker) run() {
	for {
		select {
		case item := <-w.commands:
			if item.ctx.Err() != nil {
				logging.Info("Node command cancelled before execution", types.Nodes,
					"node_id", w.nodeId)
				w.wg.Done()
				continue
			}
			if err := item.cmd.Execute(item.ctx, w); err != nil {
				logging.Error("Node command execution failed", types.Nodes,
					"node_id", w.nodeId, "error", err)
			}
			w.wg.Done()
		case <-w.shutdown:
			// Drain remaining commands before shutting down
			close(w.commands)
			for item := range w.commands {
				if err := item.cmd.Execute(item.ctx, w); err != nil {
					logging.Error("Node command execution failed during shutdown", types.Nodes,
						"node_id", w.nodeId, "error", err)
				}
				w.wg.Done()
			}
			return
		}
	}
}

// Submit queues a command for execution on this node
func (w *NodeWorker) Submit(ctx context.Context, cmd NodeWorkerCommand) bool {
	w.wg.Add(1)
	select {
	case w.commands <- commandWithContext{cmd: cmd, ctx: ctx}:
		return true
	default:
		w.wg.Done()
		return false
	}
}

// Shutdown gracefully stops the worker
func (w *NodeWorker) Shutdown() {
	close(w.shutdown)
	w.wg.Wait() // Wait for all pending commands to complete
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

// RemoveWorker removes and shuts down a worker
func (g *NodeWorkGroup) RemoveWorker(nodeId string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if worker, exists := g.workers[nodeId]; exists {
		worker.Shutdown()
		delete(g.workers, nodeId)
	}
}

// GetWorker returns a specific worker (useful for node-specific commands)
func (g *NodeWorkGroup) GetWorker(nodeId string) (*NodeWorker, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	worker, exists := g.workers[nodeId]
	return worker, exists
}
