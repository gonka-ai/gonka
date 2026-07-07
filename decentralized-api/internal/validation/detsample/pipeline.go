package detsample

// Decimal pipeline: logprob strings -> integer weights (contract §4). This is a
// line-for-line port of the vLLM reference logprobs_to_weights, using
// cockroachdb/apd (General Decimal Arithmetic, same spec as CPython's libmpdec)
// under prec=10 / ROUND_HALF_EVEN. Bit-identical results are verified against
// the shared conformance vectors.

import (
	"fmt"
	"sort"

	"github.com/cockroachdb/apd/v2"
)

const weightScale = 65536

// newCtx returns the pinned decimal context (contract §2): prec=10, HALF_EVEN.
func newCtx() *apd.Context {
	c := apd.BaseContext.WithPrecision(10)
	c.Rounding = apd.RoundHalfEven
	return c
}

func parseDec(s string) (*apd.Decimal, error) {
	d, _, err := apd.NewFromString(s)
	if err != nil {
		return nil, fmt.Errorf("detsample: bad decimal %q: %w", s, err)
	}
	return d, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// stableSortByProbDesc mirrors Python's sorted(tids, key=probs, reverse=True):
// a stable descending sort. Input must already be in lexicographic order so
// ties keep that order.
func stableSortByProbDesc(tids []string, probs map[string]*apd.Decimal) []string {
	out := append([]string(nil), tids...)
	sort.SliceStable(out, func(i, j int) bool {
		return probs[out[i]].Cmp(probs[out[j]]) > 0
	})
	return out
}

// LogprobsToWeights reproduces the reference pipeline (contract §4). logprobs
// are canonical decimal strings (contract §1). temperature must be > 0.
func LogprobsToWeights(
	logprobs map[string]string,
	temperature string,
	topP *string,
	topK *int,
	minP *string,
) (map[string]int64, error) {
	c := newCtx()

	T, err := parseDec(temperature)
	if err != nil {
		return nil, err
	}

	tids := sortedKeys(logprobs)
	if len(tids) == 0 {
		return nil, fmt.Errorf("detsample: empty logprobs")
	}

	// Temperature scaling: scaled[t] = Decimal(logprob[t]) / T.
	scaled := make(map[string]*apd.Decimal, len(tids))
	for _, tid := range tids {
		lp, err := parseDec(logprobs[tid])
		if err != nil {
			return nil, err
		}
		d := new(apd.Decimal)
		if _, err := c.Quo(d, lp, T); err != nil {
			return nil, err
		}
		scaled[tid] = d
	}

	// Softmax with max-shift.
	maxVal := scaled[tids[0]]
	for _, tid := range tids[1:] {
		if scaled[tid].Cmp(maxVal) > 0 {
			maxVal = scaled[tid]
		}
	}
	exps := make(map[string]*apd.Decimal, len(tids))
	for _, tid := range tids {
		shifted := new(apd.Decimal)
		if _, err := c.Sub(shifted, scaled[tid], maxVal); err != nil {
			return nil, err
		}
		e := new(apd.Decimal)
		if _, err := c.Exp(e, shifted); err != nil {
			return nil, err
		}
		exps[tid] = e
	}
	totalExp := new(apd.Decimal)
	for _, tid := range tids {
		if _, err := c.Add(totalExp, totalExp, exps[tid]); err != nil {
			return nil, err
		}
	}
	probs := make(map[string]*apd.Decimal, len(tids))
	for _, tid := range tids {
		p := new(apd.Decimal)
		if _, err := c.Quo(p, exps[tid], totalExp); err != nil {
			return nil, err
		}
		probs[tid] = p
	}

	// top_k
	if topK != nil && *topK < len(tids) {
		keep := stableSortByProbDesc(tids, probs)[:*topK]
		probs = subset(probs, keep)
		tids = sortedStrings(keep)
	}

	// top_p
	if topP != nil {
		tp, err := parseDec(*topP)
		if err != nil {
			return nil, err
		}
		order := stableSortByProbDesc(tids, probs)
		cumsum := new(apd.Decimal)
		var kept []string
		for _, tid := range order {
			if _, err := c.Add(cumsum, cumsum, probs[tid]); err != nil {
				return nil, err
			}
			kept = append(kept, tid)
			if cumsum.Cmp(tp) >= 0 {
				break
			}
		}
		probs = subset(probs, kept)
		tids = sortedStrings(kept)
	}

	// min_p
	if minP != nil {
		mp, err := parseDec(*minP)
		if err != nil {
			return nil, err
		}
		maxProb := probs[tids[0]]
		for _, tid := range tids[1:] {
			if probs[tid].Cmp(maxProb) > 0 {
				maxProb = probs[tid]
			}
		}
		threshold := new(apd.Decimal)
		if _, err := c.Mul(threshold, maxProb, mp); err != nil {
			return nil, err
		}
		var kept []string
		for _, tid := range tids {
			if probs[tid].Cmp(threshold) >= 0 {
				kept = append(kept, tid)
			}
		}
		if len(kept) == 0 {
			best := tids[0]
			for _, tid := range tids[1:] {
				if probs[tid].Cmp(probs[best]) > 0 {
					best = tid
				}
			}
			kept = []string{best}
		}
		probs = subset(probs, kept)
		tids = sortedStrings(kept)
	}

	// Re-normalize over survivors.
	keptTotal := new(apd.Decimal)
	for _, tid := range tids {
		if _, err := c.Add(keptTotal, keptTotal, probs[tid]); err != nil {
			return nil, err
		}
	}
	norm := make(map[string]*apd.Decimal, len(tids))
	for _, tid := range tids {
		n := new(apd.Decimal)
		if _, err := c.Quo(n, probs[tid], keptTotal); err != nil {
			return nil, err
		}
		norm[tid] = n
	}

	// Quantize to integer weights: int((norm * 65536).to_integral_value()).
	scale := apd.New(weightScale, 0)
	weights := make(map[string]int64, len(tids))
	for _, tid := range tids {
		prod := new(apd.Decimal)
		if _, err := c.Mul(prod, norm[tid], scale); err != nil {
			return nil, err
		}
		q := new(apd.Decimal)
		if _, err := c.Quantize(q, prod, 0); err != nil {
			return nil, err
		}
		wi, err := q.Int64()
		if err != nil {
			return nil, fmt.Errorf("detsample: weight not an int64 for %q: %w", tid, err)
		}
		weights[tid] = wi
	}

	// Residual fix: assign 65536 - sum to the token with the largest
	// (weight, token_id_str) tuple.
	var sum int64
	for _, tid := range tids {
		sum += weights[tid]
	}
	residual := int64(weightScale) - sum
	maxTid := tids[0]
	for _, tid := range tids[1:] {
		if weights[tid] > weights[maxTid] ||
			(weights[tid] == weights[maxTid] && tid > maxTid) {
			maxTid = tid
		}
	}
	weights[maxTid] += residual

	return weights, nil
}

// DecimalSampleFromLogprobs runs the full pipeline and samples a token
// (contract §4 + §6). The weight list is built in lexicographic token-ID order;
// the returned index maps back through the same order.
func DecimalSampleFromLogprobs(
	logprobs map[string]string,
	rng *Sha256CounterRNG,
	temperature string,
	topP *string,
	topK *int,
	minP *string,
) (string, error) {
	weights, err := LogprobsToWeights(logprobs, temperature, topP, topK, minP)
	if err != nil {
		return "", err
	}
	tids := make([]string, 0, len(weights))
	for tid := range weights {
		tids = append(tids, tid)
	}
	sort.Strings(tids)
	wl := make([]int64, len(tids))
	for i, tid := range tids {
		wl[i] = weights[tid]
	}
	idx, err := SampleCategoricalWeights(wl, rng)
	if err != nil {
		return "", err
	}
	return tids[idx], nil
}

func subset(m map[string]*apd.Decimal, keys []string) map[string]*apd.Decimal {
	out := make(map[string]*apd.Decimal, len(keys))
	for _, k := range keys {
		out[k] = m[k]
	}
	return out
}

func sortedStrings(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}
