package headertime

// DueSide is one clock's answer on a legacy shadow sample. Stamp sides carry
// the Decide result when the caller has one. Wall is set when that side is
// the wall clock.
type DueSide struct {
	Due      bool
	Decision Decision // zero when this side used the wall clock
	Wall     bool     // true when Due came from the wall clock
}

// DueEval is the sample RecordClockMismatch stores. Active is the clock that
// acted. Shadow is the stamp side of a legacy sample. Cause comes from
// CompareLegacyShadow. The acting deadline is Due.
type DueEval struct {
	Active    DueSide
	Shadow    DueSide
	HasShadow bool
	Mismatch  bool
	Cause     string
}
