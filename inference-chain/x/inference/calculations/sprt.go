package calculations

import (
	"fmt"
	"sync"

	"github.com/shopspring/decimal"
)

type Decision int

const (
	Undetermined Decision = iota
	Pass
	Fail
	Error
)

const maxLnCacheEntries = 8

var (
	lnCacheMu sync.RWMutex
	lnCache   = make(map[string]struct{ logFail, logPass decimal.Decimal })
)

type SPRT struct {
	P0, P1 decimal.Decimal // hypothesized failure probs under H0 and H1
	H      decimal.Decimal // symmetric threshold (±H)
	LLR    decimal.Decimal // running log-likelihood ratio

	// Precomputed adjustments
	// logFail  = ln(P1 / P0)
	// logPass  = ln((1 - P1) / (1 - P0))
	logFail decimal.Decimal
	logPass decimal.Decimal
}

func lnCacheKey(p0, p1 decimal.Decimal) string {
	return p0.String() + "|" + p1.String()
}

func getOrComputeLn(p0, p1 decimal.Decimal, prec int32) (logFail, logPass decimal.Decimal, err error) {
	key := lnCacheKey(p0, p1)
	lnCacheMu.RLock()
	if c, ok := lnCache[key]; ok {
		logFail, logPass = c.logFail, c.logPass
		lnCacheMu.RUnlock()
		return logFail, logPass, nil
	}
	lnCacheMu.RUnlock()

	rFail := p1.Div(p0)
	logFail, err = rFail.Ln(prec)
	if err != nil {
		return decimal.Decimal{}, decimal.Decimal{}, fmt.Errorf("ln(P1/P0): %w", err)
	}
	rPass := one.Sub(p1).Div(one.Sub(p0))
	logPass, err = rPass.Ln(prec)
	if err != nil {
		return decimal.Decimal{}, decimal.Decimal{}, fmt.Errorf("ln((1-P1)/(1-P0)): %w", err)
	}

	lnCacheMu.Lock()
	if len(lnCache) >= maxLnCacheEntries {
		// Evict one arbitrary entry to avoid unbounded growth
		for k := range lnCache {
			delete(lnCache, k)
			break
		}
	}
	lnCache[key] = struct{ logFail, logPass decimal.Decimal }{logFail: logFail, logPass: logPass}
	lnCacheMu.Unlock()
	return logFail, logPass, nil
}

func NewSPRT(p0, p1, h, llr decimal.Decimal, prec int32) (*SPRT, error) {

	// Basic sanity: keep probs in (0,1)
	if !p0.GreaterThan(decimal.Zero) || !p0.LessThan(one) ||
		!p1.GreaterThan(decimal.Zero) || !p1.LessThan(one) {
		return nil, fmt.Errorf("P0 and P1 must be in (0,1)")
	}

	logFail, logPass, err := getOrComputeLn(p0, p1, prec)
	if err != nil {
		return nil, err
	}

	return &SPRT{
		P0:      p0,
		P1:      p1,
		H:       h,
		LLR:     llr,
		logFail: logFail,
		logPass: logPass,
	}, nil
}

// UpdateCounts applies a batch: `failures` and `passes` since last call.
// LLR += failures*logFail + passes*logPass
func (s *SPRT) UpdateCounts(failures, passes int64) {
	if failures <= 0 && passes <= 0 {
		return
	}
	if failures != 0 {
		s.LLR = s.LLR.Add(s.logFail.Mul(decimal.NewFromInt(failures)))
	}
	if passes != 0 {
		s.LLR = s.LLR.Add(s.logPass.Mul(decimal.NewFromInt(passes)))
	}
}

func (s *SPRT) UpdateOne(measurementFailed bool) {
	if measurementFailed {
		s.LLR = s.LLR.Add(s.logFail)
	} else {
		s.LLR = s.LLR.Add(s.logPass)
	}
}

// Decision uses symmetric thresholds ±H
func (s *SPRT) Decision() Decision {
	if s.LLR.GreaterThanOrEqual(s.H) {
		return Fail // favor H1 (reject H0)
	}
	if s.LLR.LessThanOrEqual(s.H.Neg()) {
		return Pass // favor H0
	}
	return Undetermined
}
