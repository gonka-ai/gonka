package bridge

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

type stubDefaults struct {
	seal  uint32
	clear uint32
}

func (s stubDefaults) DefaultSealGraceNonces() uint32              { return s.seal }
func (s stubDefaults) DefaultInferenceClearGraceSeconds() uint32 { return s.clear }

func TestResolveBindGrace_ExplicitValues(t *testing.T) {
	seal, clear := ResolveBindGrace(88, 45, 3)
	assert.Equal(t, uint32(88), seal)
	assert.Equal(t, uint32(45), clear)
}

func TestResolveBindGrace_ZeroSealUsesGroupFallback(t *testing.T) {
	seal, clear := ResolveBindGrace(0, 120, 3)
	assert.Equal(t, uint32(30), seal)
	assert.Equal(t, uint32(120), clear)
}

func TestResolveBindGrace_ZeroClearUsesModuleDefault(t *testing.T) {
	seal, clear := ResolveBindGrace(10, 0, 3)
	assert.Equal(t, uint32(10), seal)
	assert.Equal(t, uint32(120), clear)
}

func TestStubDefaultsImplementsInterface(t *testing.T) {
	var _ DevshardDefaults = stubDefaults{}
}
