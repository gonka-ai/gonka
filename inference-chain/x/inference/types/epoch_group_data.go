package types

func (egd *EpochGroupData) IsModelGroup() bool {
	return egd.ModelId != ""
}
