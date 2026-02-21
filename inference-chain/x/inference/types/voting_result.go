package types

// IsValid returns true if the VoteType is a valid vote (positive or negative).
// VoteInvalid is not accepted as a valid vote.
func (v VoteType) IsValid() bool {
	return v == VoteType_VoteNegative || v == VoteType_VotePositive
}
