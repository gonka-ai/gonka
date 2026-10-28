package limits

// dimension names the congestion window a signal blames. See README.md, "What blames which window".
type dimension string

const (
	dimensionNone   dimension = "none"
	dimensionInput  dimension = "input"
	dimensionOutput dimension = "output"
	dimensionBoth   dimension = "both"
)

// tier is how hard a congestion signal narrows the window it blames.
type tier string

const (
	tierNone   tier = "none"
	tierSoft   tier = "soft"
	tierHard   tier = "hard"
	tierSevere tier = "severe"
)

// breakerEffect is what a verdict does to the cutoff.
type breakerEffect int

const (
	breakerUntouched breakerEffect = iota
	breakerRecovers
	breakerCounts
)

// TokenCost is what one attempt takes from a host: the input it must prefill and the output it may produce.
type TokenCost struct {
	Input  int64
	Output int64
}

// Pressure is a host's current latency p75 over the best it has held, per dimension. Zero means not yet known.
type Pressure struct {
	Input  float64
	Output float64
}

// Result is what one finished attempt tells the limiter.
type Result struct {
	Participant string
	Model       string
	Verdict     Verdict
	Carried     TokenCost
	Pressure    Pressure
}

// response is the whole of what one verdict means: how hard to narrow, which window to blame, and what the breaker does.
type response struct {
	tier      tier
	dimension dimension
	breaker   breakerEffect
}

func responseFor(verdict Verdict) response {
	switch verdict {
	case Success, LateSuccess:
		return response{tier: tierNone, dimension: dimensionNone, breaker: breakerRecovers}
	case Overload:
		return response{tier: tierSoft, dimension: dimensionBoth, breaker: breakerUntouched}
	case UpstreamFault, EmptyAnswer:
		return response{tier: tierHard, dimension: dimensionBoth, breaker: breakerUntouched}
	case EmptyAnswerLeftOpen:
		return response{tier: tierSevere, dimension: dimensionBoth, breaker: breakerCounts}
	case TransportFault:
		return response{tier: tierNone, dimension: dimensionNone, breaker: breakerCounts}
	case DecodeStalled:
		return response{tier: tierSevere, dimension: dimensionOutput, breaker: breakerUntouched}
	case MissedReceiptDeadline:
		return response{tier: tierSevere, dimension: dimensionBoth, breaker: breakerUntouched}
	case MissedFirstTokenDeadline:
		return response{tier: tierSevere, dimension: dimensionInput, breaker: breakerUntouched}
	}
	return response{tier: tierNone, dimension: dimensionNone, breaker: breakerUntouched}
}

// inert reports a verdict the limiter has nothing to do with, so it can return before taking the lock or creating state.
func (r response) inert() bool {
	return r.tier == tierNone && r.dimension == dimensionNone && r.breaker == breakerUntouched
}

// CongestionFactors is the β ladder: what a narrowing multiplies the window it blames by, and what the other window takes.
type CongestionFactors struct {
	Soft   float64
	Hard   float64
	Severe float64
	Cross  float64
}

func (f CongestionFactors) blamedFactor(narrowingTier tier) float64 {
	switch narrowingTier {
	case tierSoft:
		return f.Soft
	case tierHard:
		return f.Hard
	case tierSevere:
		return f.Severe
	}
	return 1
}

// narrowing is the factor each window takes from one signal; one leaves a window where it is.
type narrowing struct {
	input  float64
	output float64
}

func (n narrowing) moves() bool {
	return n.input != 1 || n.output != 1
}

// narrowingFor spreads one tier over the two windows.
func (f CongestionFactors) narrowingFor(narrowingTier tier, blamed dimension) narrowing {
	factor := f.blamedFactor(narrowingTier)
	switch {
	case narrowingTier == tierNone || blamed == dimensionNone:
		return narrowing{input: 1, output: 1}
	case blamed == dimensionBoth:
		return narrowing{input: factor, output: factor}
	case blamed == dimensionInput:
		return narrowing{input: factor, output: f.Cross}
	case blamed == dimensionOutput:
		return narrowing{input: f.Cross, output: factor}
	}
	return narrowing{input: 1, output: 1}
}

// congestedDimension names the windows a host's own latency baseline reports as congested.
func (p Pressure) congestedDimension(slack float64) dimension {
	ceiling := 1 + slack
	input := p.Input > ceiling
	output := p.Output > ceiling
	switch {
	case input && output:
		return dimensionBoth
	case input:
		return dimensionInput
	case output:
		return dimensionOutput
	}
	return dimensionNone
}

// grownBy widens one window by a whole request for an answer that used it. See README.md, "Additive increase".
func grownBy(window float64, tokens int64, step float64) float64 {
	if window <= 0 || tokens <= 0 || step <= 0 {
		return window
	}
	return window + step
}

// narrowedTo applies one factor, never below the floor and never below one token.
func narrowedTo(window float64, factor float64, floor float64) float64 {
	return max(window*factor, max(floor, 1))
}
