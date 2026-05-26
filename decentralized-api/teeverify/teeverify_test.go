package teeverify

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubVerifier struct {
	provider string
	report   *Report
	err      error
	calls    int
}

func (s *stubVerifier) Provider() string { return s.provider }
func (s *stubVerifier) Verify(_ context.Context, _ VerifyRequest) (*Report, error) {
	s.calls++
	return s.report, s.err
}

func TestRegistry_RegisterAndLookup(t *testing.T) {
	r := NewRegistry()
	a := &stubVerifier{provider: "intel-tdx"}
	b := &stubVerifier{provider: "amd-sev-snp"}
	r.Register(a)
	r.Register(b)

	got, ok := r.Lookup("intel-tdx")
	require.True(t, ok)
	assert.Same(t, a, got)

	got, ok = r.Lookup("amd-sev-snp")
	require.True(t, ok)
	assert.Same(t, b, got)

	_, ok = r.Lookup("snake-oil")
	assert.False(t, ok)
}

func TestRegistry_RegisterDuplicatePanics(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubVerifier{provider: "intel-tdx"})

	assert.PanicsWithValue(t,
		"teeverify: duplicate Verifier registered for provider \"intel-tdx\"",
		func() { r.Register(&stubVerifier{provider: "intel-tdx"}) },
	)
}

func TestRegistry_RegisterNilPanics(t *testing.T) {
	r := NewRegistry()
	assert.PanicsWithValue(t,
		"teeverify: Register called with nil Verifier",
		func() { r.Register(nil) },
	)
}

func TestRegistry_RegisterEmptyProviderPanics(t *testing.T) {
	r := NewRegistry()
	assert.PanicsWithValue(t,
		"teeverify: Verifier.Provider() returned an empty string",
		func() { r.Register(&stubVerifier{provider: ""}) },
	)
}

func TestRegistry_ProvidersSorted(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubVerifier{provider: "intel-tdx-lite"})
	r.Register(&stubVerifier{provider: "amd-sev-snp"})
	r.Register(&stubVerifier{provider: "nvidia-cc"})
	assert.Equal(t, []string{"amd-sev-snp", "intel-tdx-lite", "nvidia-cc"}, r.Providers())
}

func TestNewDefaultRegistry_RegistersIntelTDXLite(t *testing.T) {
	r := NewDefaultRegistry()
	v, ok := r.Lookup(IntelTDXLiteProvider)
	require.True(t, ok)
	assert.Equal(t, IntelTDXLiteProvider, v.Provider())
}

func TestNewDefaultRegistry_DoesNotRegisterStrictIntelTDX(t *testing.T) {
	r := NewDefaultRegistry()
	_, ok := r.Lookup(IntelTDXProvider)
	assert.False(t, ok)
}
