package contract

const (
	HeaderRequestID = "X-Trainshard-Request-Id"
	HeaderSignature = "X-Trainshard-Signature"
	HeaderTimestamp = "X-Trainshard-Timestamp"
)

// ShellProtocol is what a shell request asks to switch to; a proxy tunnels the connection
// both ways only after a 101 for an Upgrade, and holds back bytes sent after a plain request
const ShellProtocol = "trainshard-shell"

const (
	PathReadiness = "/trainshard/v0/readiness"
	PathMesh      = "/trainshard/v0/shards/{shard_id}/mesh"
	PathDeploy    = "/trainshard/v0/shards/{shard_id}/deploy"
	PathStart     = "/trainshard/v0/shards/{shard_id}/start"
	PathStop      = "/trainshard/v0/shards/{shard_id}/stop"
	PathStatus    = "/trainshard/v0/shards/{shard_id}/status"
	PathReport    = "/trainshard/v0/shards/{shard_id}/report"
	PathProbe     = "/trainshard/v0/shards/{shard_id}/nodes/{node_id}/probe"
	PathLogs      = "/trainshard/v0/shards/{shard_id}/nodes/{node_id}/logs"
	PathShell     = "/trainshard/v0/shards/{shard_id}/nodes/{node_id}/shell"
)
