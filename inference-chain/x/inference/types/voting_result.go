package types

// IsValid returns true if the VoteType is a recognized value.
func (v VoteType) IsValid() bool {
	return v == VoteType_VoteInvalid || v == VoteType_VoteNegative || v == VoteType_VotePositive
}
