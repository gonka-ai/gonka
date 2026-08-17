package observability

type (
	tracerID string
	spanID   string
)

type tracerNames struct {
	Chain tracerID
}

type chainSpanNames struct {
	StoreQuery spanID
	GRPCQuery  spanID
}

var tracerName = tracerNames{
	Chain: "common.chain",
}

var spanName = struct {
	Chain chainSpanNames
}{
	Chain: chainSpanNames{
		StoreQuery: "chain.store.query",
		GRPCQuery:  "chain.grpc.query",
	},
}
