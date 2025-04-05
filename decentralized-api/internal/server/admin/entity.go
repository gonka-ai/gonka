package admin

type UnitOfComputePriceProposalDto struct {
	Price uint64 `json:"price"`
	Denom string `json:"denom"`
}

type RegisterModelDto struct {
	Id                     string `json:"id"`
	UnitsOfComputePerToken uint64 `json:"units_of_compute_per_token"`
}
