package types

func (n *MLNodeInfo) IsActiveDuringPoC() bool {
	if n == nil {
		return false
	}

	return len(n.TimeslotAllocation) > 1 && n.TimeslotAllocation[1]
}
