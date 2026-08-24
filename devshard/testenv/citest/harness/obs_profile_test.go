package harness

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestObsProfile_OTELEndpointMatrix(t *testing.T) {
	cases := map[ObsProfile]string{
		ObsProfileTempoAlloy:     "http://alloy:4317",
		ObsProfileJaegerAlloy:    "http://alloy:4317",
		ObsProfileTempoPromtail:  "http://tempo:4317",
		ObsProfileJaegerPromtail: "http://jaeger:4317",
	}
	for profile, want := range cases {
		require.Equal(t, want, profile.OTELEndpoint(), "profile %s", profile)
	}
}

func TestObsProfile_ComposeFragments(t *testing.T) {
	tempoAlloy := ObsProfileTempoAlloy.ComposeFragmentNames()
	require.Equal(t, []string{
		"docker-compose.observability.tempo.yml",
		"docker-compose.observability.alloy.yml",
	}, tempoAlloy)

	jaegerPT := ObsProfileJaegerPromtail.ComposeFragmentNames()
	require.Equal(t, []string{
		"docker-compose.observability.jaeger.yml",
		"docker-compose.observability.promtail.yml",
	}, jaegerPT)

	require.Equal(t, "tempo", ObsProfileTempoAlloy.TraceBackend())
	require.Equal(t, "jaeger", ObsProfileJaegerPromtail.TraceBackend())
	require.True(t, ObsProfileTempoAlloy.UsesAlloy())
	require.False(t, ObsProfileTempoAlloy.UsesPromtail())
	require.True(t, ObsProfileJaegerPromtail.UsesPromtail())
}

func TestObsProfile_IPServices(t *testing.T) {
	have := map[string]struct{}{}
	for _, s := range ObsProfileTempoAlloy.IPServices() {
		have[s] = struct{}{}
	}
	for _, want := range []string{"prometheus", "loki", "grafana", "tempo", "alloy"} {
		_, ok := have[want]
		require.True(t, ok, "missing %s", want)
	}
	_, hasJaeger := have["jaeger"]
	_, hasPromtail := have["promtail"]
	require.False(t, hasJaeger)
	require.False(t, hasPromtail)

	haveJP := map[string]struct{}{}
	for _, s := range ObsProfileJaegerPromtail.IPServices() {
		haveJP[s] = struct{}{}
	}
	_, ok := haveJP["jaeger"]
	require.True(t, ok)
	_, ok = haveJP["promtail"]
	require.True(t, ok)
	_, hasTempo := haveJP["tempo"]
	require.False(t, hasTempo)
}

func TestResolveObsProfile_DefaultAndEnv(t *testing.T) {
	t.Setenv(envObsProfile, "")
	require.Equal(t, DefaultObsProfile, ResolveObsProfile())

	t.Setenv(envObsProfile, "jaeger-promtail")
	require.Equal(t, ObsProfileJaegerPromtail, ResolveObsProfile())

	t.Setenv(envObsProfile, "not-a-profile")
	require.Equal(t, DefaultObsProfile, ResolveObsProfile())
}
