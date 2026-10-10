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
	cases := []struct {
		profile  ObsProfile
		backend  string
		alloy    bool
		promtail bool
		frags    []string
	}{
		{ObsProfileTempoAlloy, "tempo", true, false, []string{
			"docker-compose.observability.tempo.yml",
			"docker-compose.observability.alloy.yml",
		}},
		{ObsProfileTempoPromtail, "tempo", false, true, []string{
			"docker-compose.observability.tempo.yml",
			"docker-compose.observability.promtail.yml",
		}},
		{ObsProfileJaegerAlloy, "jaeger", true, false, []string{
			"docker-compose.observability.jaeger.yml",
			"docker-compose.observability.alloy.yml",
		}},
		{ObsProfileJaegerPromtail, "jaeger", false, true, []string{
			"docker-compose.observability.jaeger.yml",
			"docker-compose.observability.promtail.yml",
		}},
	}
	for _, tc := range cases {
		t.Run(string(tc.profile), func(t *testing.T) {
			require.Equal(t, tc.frags, tc.profile.ComposeFragmentNames())
			require.Equal(t, tc.backend, tc.profile.TraceBackend())
			require.Equal(t, tc.alloy, tc.profile.UsesAlloy())
			require.Equal(t, tc.promtail, tc.profile.UsesPromtail())
		})
	}
	require.Equal(t, "tempo", ObsProfile("").TraceBackend())
}

func TestObsProfile_IPServices(t *testing.T) {
	cases := []struct {
		profile ObsProfile
		have    []string
		absent  []string
	}{
		{ObsProfileTempoAlloy, []string{"prometheus", "loki", "grafana", "tempo", "alloy"}, []string{"jaeger", "promtail"}},
		{ObsProfileTempoPromtail, []string{"prometheus", "loki", "grafana", "tempo", "promtail"}, []string{"jaeger", "alloy"}},
		{ObsProfileJaegerAlloy, []string{"prometheus", "loki", "grafana", "jaeger", "alloy"}, []string{"tempo", "promtail"}},
		{ObsProfileJaegerPromtail, []string{"prometheus", "loki", "grafana", "jaeger", "promtail"}, []string{"tempo", "alloy"}},
	}
	for _, tc := range cases {
		t.Run(string(tc.profile), func(t *testing.T) {
			got := map[string]struct{}{}
			for _, s := range tc.profile.IPServices() {
				got[s] = struct{}{}
			}
			for _, want := range tc.have {
				_, ok := got[want]
				require.True(t, ok, "missing %s", want)
			}
			for _, ban := range tc.absent {
				_, ok := got[ban]
				require.False(t, ok, "profile %s must not start %s", tc.profile, ban)
			}
		})
	}
}

func TestObsProfile_HostPublishedPorts(t *testing.T) {
	cases := []struct {
		profile ObsProfile
		have    []string
		absent  []string
	}{
		{ObsProfileTempoAlloy, []string{"loki", "prometheus", "grafana", "tempo", "alloy"}, []string{"jaeger", "promtail"}},
		{ObsProfileTempoPromtail, []string{"loki", "prometheus", "grafana", "tempo"}, []string{"jaeger", "alloy", "promtail"}},
		{ObsProfileJaegerAlloy, []string{"loki", "prometheus", "grafana", "jaeger", "alloy"}, []string{"tempo", "promtail"}},
		{ObsProfileJaegerPromtail, []string{"loki", "prometheus", "grafana", "jaeger"}, []string{"tempo", "alloy", "promtail"}},
	}
	for _, tc := range cases {
		t.Run(string(tc.profile), func(t *testing.T) {
			got := map[string]int{}
			for _, pub := range tc.profile.hostPublishedPorts() {
				got[pub.service] = pub.port
			}
			for _, want := range tc.have {
				_, ok := got[want]
				require.True(t, ok, "missing published port for %s", want)
			}
			for _, ban := range tc.absent {
				_, ok := got[ban]
				require.False(t, ok, "must not publish %s", ban)
			}
			if _, ok := got["tempo"]; ok {
				require.Equal(t, 3200, got["tempo"])
			}
			if _, ok := got["jaeger"]; ok {
				require.Equal(t, 16686, got["jaeger"])
			}
			if _, ok := got["alloy"]; ok {
				require.Equal(t, 12345, got["alloy"])
			}
		})
	}
}

func TestResolveObsProfile_DefaultAndEnv(t *testing.T) {
	t.Setenv(envObsProfile, "")
	require.Equal(t, DefaultObsProfile, ResolveObsProfile())

	t.Setenv(envObsProfile, "jaeger-promtail")
	require.Equal(t, ObsProfileJaegerPromtail, ResolveObsProfile())

	t.Setenv(envObsProfile, "not-a-profile")
	require.Equal(t, DefaultObsProfile, ResolveObsProfile())
}
