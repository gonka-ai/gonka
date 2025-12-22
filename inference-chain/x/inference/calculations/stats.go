package calculations

import (
	"errors"

	"github.com/shopspring/decimal"
)

var (
	zero  = decimal.NewFromInt(0)
	alpha = decimal.NewFromFloat(0.05)
)

// BinomialPValue computes P(X >= k) for X ~ Binomial(n, p0), one-sided "greater" test.
func BinomialPValue(k, n int, p0 decimal.Decimal, prec int32) (decimal.Decimal, error) {
	if k < 0 || n < 0 || k > n {
		return zero, errors.New("invalid input: requires 0 <= k <= n")
	}
	if p0.LessThanOrEqual(zero) || p0.GreaterThanOrEqual(one) {
		return zero, errors.New("p0 must be in (0, 1)")
	}

	if k == 0 {
		return one, nil
	}

	q0 := one.Sub(p0)
	prob := binomialPMF(k, n, p0, q0, prec)
	sum := prob

	ratio := p0.Div(q0)
	for i := k; i < n; i++ {
		factor := decimal.NewFromInt(int64(n - i)).Div(decimal.NewFromInt(int64(i + 1)))
		prob = prob.Mul(factor).Mul(ratio)
		sum = sum.Add(prob)
	}

	return sum.Round(prec), nil
}

// binomialPMF computes P(X = k) = C(n,k) * p^k * (1-p)^(n-k).
func binomialPMF(k, n int, p, q decimal.Decimal, prec int32) decimal.Decimal {
	if k == 0 {
		return pow(q, n, prec)
	}
	if k == n {
		return pow(p, n, prec)
	}

	coeff := one
	for i := 0; i < k; i++ {
		coeff = coeff.Mul(decimal.NewFromInt(int64(n - i))).Div(decimal.NewFromInt(int64(i + 1)))
	}

	pPowK := pow(p, k, prec)
	qPowNK := pow(q, n-k, prec)
	return coeff.Mul(pPowK).Mul(qPowNK)
}

// pow computes base^exp for non-negative integer exponents.
func pow(base decimal.Decimal, exp int, prec int32) decimal.Decimal {
	if exp == 0 {
		return one
	}
	if exp == 1 {
		return base
	}

	result := one
	b := base
	for exp > 0 {
		if exp&1 == 1 {
			result = result.Mul(b)
		}
		b = b.Mul(b)
		exp >>= 1
	}
	return result.Round(prec)
}

// MissedStatTest returns true if miss rate is acceptable (p-value >= alpha).
// Uses pre-computed tables for supported p0 values (0.05, 0.10, 0.20, 0.30, 0.40, 0.50).
func MissedStatTest(nMissed, nTotal int, p0 decimal.Decimal) (bool, error) {
	if nTotal == 0 {
		return true, nil
	}
	if nMissed < 0 || nTotal < 0 || nMissed > nTotal {
		return false, errors.New("invalid input: requires 0 <= nMissed <= nTotal and nTotal > 0")
	}

	permille := decimalToPermille(p0)
	if permille > 0 {
		return MissedStatTestLookupWithP0(nMissed, nTotal, permille)
	}

	const precision int32 = 16
	pValue, err := BinomialPValue(nMissed, nTotal, p0, precision)
	if err != nil {
		return false, err
	}
	return pValue.GreaterThanOrEqual(alpha), nil
}
