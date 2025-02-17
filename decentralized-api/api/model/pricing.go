package model

type UnitOfComputePriceProposalDto struct {
	Price uint64 `json:"price"`
	Denom string `json:"denom"`
}

type PricingDto struct {
	Price  uint64          `json:"unit_of_compute_price"`
	Models []ModelPriceDto `json:"models"`
}

type RegisterModelDto struct {
	Id                     string `json:"id"`
	UnitsOfComputePerToken uint64 `json:"units_of_compute_per_token"`
}

type ModelPriceDto struct {
	Id                     string `json:"id"`
	UnitsOfComputePerToken uint64 `json:"units_of_compute_per_token"`
	PricePerToken          uint64 `json:"price_per_token"`
}
