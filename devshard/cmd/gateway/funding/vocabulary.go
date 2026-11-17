package funding

// Action is what the planner wants done with an escrow; the metrics label carries it.
type Action string

const (
	ActionCreate Action = "create"
	ActionRetire Action = "retire"
)

// Reason is why the planner wants a create or a retire; the journal and the metrics label carry it. See README.md, "The algorithm".
type Reason string

const (
	ReasonGuard          Reason = "guard"
	ReasonCapacity       Reason = "capacity"
	ReasonSpread         Reason = "spread"
	ReasonStandby        Reason = "standby"
	ReasonNonceCap       Reason = "nonce_cap"
	ReasonIdleStarved    Reason = "idle_starved"
	ReasonBudgetPressure Reason = "budget_pressure"
	ReasonSurplus        Reason = "surplus"
)
