package filters

import "fmt"

// ThinkingDisposition is the closed set of ways a profile handles the thinking/enable_thinking fields. See README.md, "Model profiles".
type ThinkingDisposition int

const (
	ThinkingNormalizeInPlace ThinkingDisposition = iota
	ThinkingMirrorToKwargs
	ThinkingStrip
	ThinkingForceOn
)

func thinkingWireOf(disposition ThinkingDisposition) (thinkingWire, bool) {
	switch disposition {
	case ThinkingNormalizeInPlace:
		return thinkingWireNormalize, true
	case ThinkingMirrorToKwargs:
		return thinkingWireMirrorToKwargs, true
	case ThinkingStrip:
		return thinkingWireStrip, true
	case ThinkingForceOn:
		return thinkingWireForceOn, true
	}
	return thinkingWireUnknown, false
}

func (disposition ThinkingDisposition) MarshalText() ([]byte, error) {
	wire, known := thinkingWireOf(disposition)
	if !known {
		return nil, fmt.Errorf("unknown thinking disposition %d", disposition)
	}
	return []byte(wire), nil
}

func (disposition *ThinkingDisposition) UnmarshalText(text []byte) error {
	for _, candidate := range [...]ThinkingDisposition{ThinkingNormalizeInPlace, ThinkingMirrorToKwargs, ThinkingStrip, ThinkingForceOn} {
		if wire, _ := thinkingWireOf(candidate); string(wire) == string(text) {
			*disposition = candidate
			return nil
		}
	}
	return fmt.Errorf("%w: thinking %q is not one of %s, %s, %s, %s", ErrInvalidModelSetting, text,
		thinkingWireNormalize, thinkingWireMirrorToKwargs, thinkingWireStrip, thinkingWireForceOn)
}
