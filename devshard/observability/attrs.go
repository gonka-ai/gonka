package observability

import "go.opentelemetry.io/otel/attribute"

const (
	AttrEscrowID       = attribute.Key("escrow.id")
	AttrNonce          = attribute.Key("devshard.nonce")
	AttrSlotID         = attribute.Key("devshard.slot_id")
	AttrModel          = attribute.Key("model")
	AttrMLNodeID       = attribute.Key("mlnode.node.id")
	AttrMLNodeEndpoint = attribute.Key("mlnode.endpoint")
	AttrMLNodeLockID   = attribute.Key("mlnode.lock_id")
)
