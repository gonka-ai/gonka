package funding

// SurplusHold keeps a model's surplus retires back for one demand window after a capacity shortfall. See README.md, "Retires".
type SurplusHold struct {
	remaining int
}

// Arm starts a full demand window of holding.
func (hold *SurplusHold) Arm() { hold.remaining = demandSamples }

// Elapse counts one scheduled tick off the window.
func (hold *SurplusHold) Elapse() { hold.remaining = max(hold.remaining-1, 0) }

// Active reports whether surplus retires are still held back.
func (hold *SurplusHold) Active() bool { return hold.remaining > 0 }
