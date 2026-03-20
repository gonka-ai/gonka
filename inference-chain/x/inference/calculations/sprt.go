package calculations

import (
	"github.com/shopspring/decimal"
)

type Decision int

const (
	Undetermined Decision = iota
	Pass
	Fail
	Error
)

type SPRT struct {
	H       decimal.Decimal // symmetric threshold (±H)
	LLR     decimal.Decimal // running log-likelihood ratio
	logFail decimal.Decimal // ln(P1 / P0)
	logPass decimal.Decimal // ln((1 - P1) / (1 - P0))
}

// UpdateCounts applies a batch: `failures` and `passes` since last call.
// LLR += failures*logFail + passes*logPass
func (s SPRT) UpdateCounts(failures, passes int64) SPRT {
	if failures <= 0 && passes <= 0 {
		return s
	}
	if failures != 0 {
		s.LLR = s.LLR.Add(s.logFail.Mul(decimal.NewFromInt(failures)))
	}
	if passes != 0 {
		s.LLR = s.LLR.Add(s.logPass.Mul(decimal.NewFromInt(passes)))
	}
	return s
}

func (s SPRT) UpdateOne(measurementFailed bool) SPRT {
	if measurementFailed {
		s.LLR = s.LLR.Add(s.logFail)
	} else {
		s.LLR = s.LLR.Add(s.logPass)
	}
	return s
}

// Decision uses symmetric thresholds ±H
func (s SPRT) Decision() Decision {
	if s.LLR.GreaterThanOrEqual(s.H) {
		return Fail // favor H1 (reject H0)
	}
	if s.LLR.LessThanOrEqual(s.H.Neg()) {
		return Pass // favor H0
	}
	return Undetermined
}
