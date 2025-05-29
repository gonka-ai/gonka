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
	mlClient *mlnodeclient.Client
	jobs     chan func() error
	shutdown chan struct{}
	wg       sync.WaitGroup
}

// NewNodeWorker creates a new worker for a specific node
func NewNodeWorker(nodeId string, node *NodeWithState) *NodeWorker {
	worker := &NodeWorker{
		nodeId:   nodeId,
		node:     node,
		mlClient: newNodeClient(&node.Node),
		jobs:     make(chan func() error, 10), // Buffer for 10 jobs
		shutdown: make(chan struct{}),
	}
	go worker.run()
	return worker
}

// run is the main event loop for the worker
func (w *NodeWorker) run() {
	for {
		select {
		case job := <-w.jobs:
			if err := job(); err != nil {
				logging.Error("Node job execution failed", types.Nodes,
					"node_id", w.nodeId, "error", err)
			}
			w.wg.Done()
		case <-w.shutdown:
			// Drain remaining jobs before shutting down
			close(w.jobs)
			for job := range w.jobs {
				if err := job(); err != nil {
					logging.Error("Node job execution failed during shutdown", types.Nodes,
						"node_id", w.nodeId, "error", err)
				}
				w.wg.Done()
			}
			return
		}
	}
}

// Submit queues a job for execution on this node
func (w *NodeWorker) Submit(job func() error) bool {
	w.wg.Add(1)
	select {
	case w.jobs <- job:
		return true
	default:
		// Queue is full
		w.wg.Done()
		return false
	}
}

// Shutdown gracefully stops the worker
func (w *NodeWorker) Shutdown() {
	close(w.shutdown)
	w.wg.Wait()
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

// ExecuteOnAll submits jobs to all workers and waits for completion
func (g *NodeWorkGroup) ExecuteOnAll(jobFactory func(nodeId string, node *NodeWithState) func() error) (submitted, failed int) {
	g.mu.RLock()
	workersCopy := make(map[string]*NodeWorker)
	for k, v := range g.workers {
		workersCopy[k] = v
	}
	g.mu.RUnlock()

	// Submit jobs to all workers
	for nodeId, worker := range workersCopy {
		job := jobFactory(nodeId, worker.node)
		if worker.Submit(job) {
			submitted++
		} else {
			failed++
			logging.Error("Failed to submit job to worker", types.Nodes,
				"node_id", nodeId, "reason", "queue full")
		}
	}

	// Wait for all submitted jobs to complete
	for _, worker := range workersCopy {
		worker.wg.Wait()
	}

	return submitted, failed
}

// ExecuteOnNodes submits jobs to specific workers and waits for completion
func (g *NodeWorkGroup) ExecuteOnNodes(nodeIds []string, jobFactory func(nodeId string, node *NodeWithState) func() error) (submitted, failed int) {
	g.mu.RLock()
	selectedWorkers := make(map[string]*NodeWorker)
	for _, nodeId := range nodeIds {
		if worker, exists := g.workers[nodeId]; exists {
			selectedWorkers[nodeId] = worker
		}
	}
	g.mu.RUnlock()

	// Submit jobs to selected workers
	for nodeId, worker := range selectedWorkers {
		job := jobFactory(nodeId, worker.node)
		if worker.Submit(job) {
			submitted++
		} else {
			failed++
			logging.Error("Failed to submit job to worker", types.Nodes,
				"node_id", nodeId, "reason", "queue full")
		}
	}

	// Wait for all submitted jobs to complete
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
