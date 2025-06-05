package broker

import (
	"decentralized-api/logging"
	"decentralized-api/mlnodeclient"
	"sync"

	"github.com/productscience/inference/x/inference/types"
)

// NodeWorker handles asynchronous operations for a specific node
type NodeWorker struct {
	nodeId   string
	node     *NodeWithState
	mlClient mlnodeclient.MLNodeClient
	commands chan NodeWorkerCommand
	shutdown chan struct{}
	wg       sync.WaitGroup
}

// NewNodeWorkerWithClient creates a new worker with a custom client (for testing)
func NewNodeWorkerWithClient(nodeId string, node *NodeWithState, client mlnodeclient.MLNodeClient) *NodeWorker {
	worker := &NodeWorker{
		nodeId:   nodeId,
		node:     node,
		mlClient: client,
		commands: make(chan NodeWorkerCommand, 10),
		shutdown: make(chan struct{}),
	}
	go worker.run()
	return worker
}

// run is the main event loop for the worker
func (w *NodeWorker) run() {
	for {
		select {
		case cmd := <-w.commands:
			if err := cmd.Execute(w); err != nil {
				logging.Error("Node command execution failed", types.Nodes,
					"node_id", w.nodeId, "error", err)
			}
			w.wg.Done()
		case <-w.shutdown:
			// Drain remaining commands before shutting down
			close(w.commands)
			for cmd := range w.commands {
				if err := cmd.Execute(w); err != nil {
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
func (w *NodeWorker) Submit(cmd NodeWorkerCommand) bool {
	w.wg.Add(1)
	select {
	case w.commands <- cmd:
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

// ExecuteOnAll submits a command to all workers and waits for completion
func (g *NodeWorkGroup) ExecuteOnAll(cmd NodeWorkerCommand) (submitted, failed int) {
	g.mu.RLock()
	workersCopy := make(map[string]*NodeWorker)
	for k, v := range g.workers {
		workersCopy[k] = v
	}
	g.mu.RUnlock()

	// Submit command to all workers
	for nodeId, worker := range workersCopy {
		if worker.Submit(cmd) {
			submitted++
		} else {
			failed++
			logging.Error("Failed to submit command to worker", types.Nodes,
				"node_id", nodeId, "reason", "queue full")
		}
	}

	// Wait for all submitted commands to complete
	for _, worker := range workersCopy {
		worker.wg.Wait()
	}

	return submitted, failed
}

// ExecuteOnNodes submits a command to specific workers and waits for completion
func (g *NodeWorkGroup) ExecuteOnNodes(nodeIds []string, cmd NodeWorkerCommand) (submitted, failed int) {
	g.mu.RLock()
	selectedWorkers := make(map[string]*NodeWorker)
	for _, nodeId := range nodeIds {
		if worker, exists := g.workers[nodeId]; exists {
			selectedWorkers[nodeId] = worker
		}
	}
	g.mu.RUnlock()

	// Submit command to selected workers
	for nodeId, worker := range selectedWorkers {
		if worker.Submit(cmd) {
			submitted++
		} else {
			failed++
			logging.Error("Failed to submit command to worker", types.Nodes,
				"node_id", nodeId, "reason", "queue full")
		}
	}

	// Wait for all submitted commands to complete
	for _, worker := range selectedWorkers {
		worker.wg.Wait()
	}

	return submitted, failed
}

// GetWorker returns a specific worker (useful for node-specific commands)
func (g *NodeWorkGroup) GetWorker(nodeId string) (*NodeWorker, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	worker, exists := g.workers[nodeId]
	return worker, exists
}
