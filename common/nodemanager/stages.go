package nodemanager

// Stage names for the node-selection log lines. Citests and Loki joins use
// these so both production dapi and mock-dapi share one correlation contract
// (trace_id / request_id on AcquireMLNode / ReleaseMLNode).
const (
	StageMLNodeAcquire = "mlnode_acquire"
	StageMLNodeRelease = "mlnode_release"
)
