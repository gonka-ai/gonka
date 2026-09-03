package contract

type Command struct {
	ShardID   string   `json:"shard_id"`
	NodeIDs   []string `json:"node_ids"`
	RequestID string   `json:"request_id"`
	Deadline  string   `json:"deadline"`
}

type DeployRequest struct {
	Command
	ImageDigest string            `json:"image_digest"`
	Args        []string          `json:"command,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Sources     []string          `json:"sources,omitempty"`
	GPUs        int               `json:"gpus"`
	DiskBytes   int64             `json:"disk_bytes"`
}

type StartRequest struct {
	Command
}

type StopRequest struct {
	Command
	GraceSeconds int `json:"grace_seconds"`
}

type StatusRequest struct {
	Command
}

type ReportRequest struct {
	Command
}

type Peer struct {
	Rank        int    `json:"rank"`
	Participant string `json:"participant"`
	NodeID      string `json:"node_id"`
	Address     string `json:"address"`
	PublicKey   string `json:"public_key"`
}

type MeshRequest struct {
	Command
	Peers []Peer `json:"peers"`
}

type LogsRequest struct {
	Since string `json:"since,omitempty"`
	Tail  int    `json:"tail,omitempty"`
}
