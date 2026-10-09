package heightsync

// QuorumForRoster returns ceil(2/3 × rosterSize) for PoC defaults.
// Used by turn completion and floor raise corroboration — not by a
// height-confirmation predicate (envelope (C-quorum) is withdrawn).
func QuorumForRoster(rosterSize int) int {
	if rosterSize <= 0 {
		return 0
	}
	return (2*rosterSize + 2) / 3
}
