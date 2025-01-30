package model

type PricingDto struct {
	Price  uint64          `json:"unit_of_compute_price"`
	Models []ModelPriceDto `json:"models"`
}

type ModelPriceDto struct {
	ModelId                string `json:"model_id"`
	UnitsOfComputePerToken uint64 `json:"units_of_compute_per_token"`
	PricePerToken          uint64 `json:"price_per_token"`
}
