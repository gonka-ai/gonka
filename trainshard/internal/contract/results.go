package contract

import "encoding/json"

type Envelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error *Error          `json:"error,omitempty"`
	Meta  Meta            `json:"meta"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Meta struct {
	RequestID string `json:"request_id"`
}

type NodeResult struct {
	NodeID      string `json:"node_id"`
	State       string `json:"state"`
	ImageDigest string `json:"image_digest,omitempty"`
	ExitCode    *int   `json:"exit_code,omitempty"`
	Error       *Error `json:"error,omitempty"`
}

type NodesResult struct {
	Items []NodeResult `json:"items"`
}

type NodeStatus struct {
	NodeResult
	Prepared       bool  `json:"prepared"`
	MeshUp         bool  `json:"mesh_up"`
	GPUsInUse      int   `json:"gpus_in_use"`
	DiskBytes      int64 `json:"disk_bytes"`
	DiskQuotaBytes int64 `json:"disk_quota_bytes"`
}

type StatusResult struct {
	Items []NodeStatus `json:"items"`
}

type NodeReadiness struct {
	NodeID string `json:"node_id"`
	Ready  bool   `json:"ready"`
	Reason string `json:"reason,omitempty"`
}

type ReadinessResult struct {
	Ready   bool            `json:"ready"`
	Reason  string          `json:"reason,omitempty"`
	Version string          `json:"version"`
	Items   []NodeReadiness `json:"items"`
}

type MeshIdentity struct {
	NodeID    string `json:"node_id"`
	Address   string `json:"address"`
	PublicKey string `json:"public_key"`
	Signature string `json:"signature"`
}

type MeshResult struct {
	Items []MeshIdentity `json:"items"`
}

type PeerRef struct {
	Participant string `json:"participant"`
	NodeID      string `json:"node_id"`
}

type ProbeResult struct {
	NodeID      string    `json:"node_id"`
	Unreachable []PeerRef `json:"unreachable"`
}

type ImageRun struct {
	ImageDigest string `json:"image_digest"`
	At          string `json:"at"`
}

type NodeReport struct {
	NodeID   string     `json:"node_id"`
	Images   []ImageRun `json:"images"`
	ExitCode *int       `json:"exit_code,omitempty"`
	Error    *Error     `json:"error,omitempty"`
}

type ReportResult struct {
	Items []NodeReport `json:"items"`
}
