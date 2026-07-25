package queryapi

import inferencetypes "github.com/productscience/inference/x/inference/types"

// governanceModelsResponse matches decentralized-api ModelsResponse JSON shape.
type governanceModelsResponse struct {
	Models []inferencetypes.Model `json:"models"`
}
