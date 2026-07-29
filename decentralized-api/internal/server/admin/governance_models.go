package admin

import (
	"context"

	cosmos_client "decentralized-api/cosmosclient"

	"github.com/productscience/inference/x/inference/types"
)

// governanceModelSource provides the chain's governance models (with their
// args). Narrow interface so the tester can merge governance args the same way
// the broker does, and so it is easy to stub in tests.
//
// The context is not decorative: the query is a chain RPC issued while an
// MLnode test holds that node's in-flight slot. Without a caller-controlled
// context a hung RPC would keep the slot held forever (the broker's own bridge
// binds every query to the process-lifetime context), leaving the node
// permanently "testing" and un-testable.
type governanceModelSource interface {
	GetGovernanceModels(ctx context.Context) (*types.QueryModelsAllResponse, error)
}

// chainGovernanceModels reads governance models straight from the chain via the
// admin server's own cosmos client, so the caller's context bounds the query.
type chainGovernanceModels struct {
	client cosmos_client.CosmosMessageClient
}

func (c chainGovernanceModels) GetGovernanceModels(ctx context.Context) (*types.QueryModelsAllResponse, error) {
	return c.client.NewInferenceQueryClient().ModelsAll(ctx, &types.QueryModelsAllRequest{})
}
