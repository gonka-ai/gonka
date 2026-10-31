package escrow

const (
	roleTemp    = "temp"
	roleRegular = "regular"
	RoleReserve = "reserve"
)

const stagePrepareTemp = "prepare_temp"

const (
	commitmentClearedNoEscrow   = "transaction created no escrow"
	commitmentClearedCannotLand = "transaction can no longer land"
	commitmentClearedRejected   = "transaction rejected at broadcast"
)

type createReason string

const (
	createdForBridge  createReason = "bridge"
	createdByOperator createReason = "operator"
)

type chainFactsFailure string

const (
	chainFactsLookupFailed chainFactsFailure = "lookup_failed"
	chainFactsNotOnChain   chainFactsFailure = "not_on_chain"
	chainFactsNoEpoch      chainFactsFailure = "no_epoch"
	chainFactsWriteFailed  chainFactsFailure = "write_failed"
)

type deadlineUnsettled string

const (
	deadlineSettlementDisabled  deadlineUnsettled = "settlement_disabled"
	deadlineOperatorDeactivated deadlineUnsettled = "operator_deactivated"
	deadlineKeyMissing          deadlineUnsettled = "key_missing"
	deadlinePassed              deadlineUnsettled = "deadline_passed"
)

// CountState is how the funding report counts one escrow of a model; the metrics label carries it. See README.md, "The funding planner".
type CountState string

const (
	CountFull      CountState = "full"
	CountStarved   CountState = "starved"
	CountSpent     CountState = "spent"
	CountStandby   CountState = "standby"
	CountUnread    CountState = "unread"
	CountParked    CountState = "parked"
	CountInactive  CountState = "inactive"
	CountCommitted CountState = "committed"
)

var countStates = []CountState{CountFull, CountStarved, CountSpent, CountStandby, CountUnread, CountParked, CountInactive, CountCommitted}
