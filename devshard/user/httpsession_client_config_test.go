package user

import (
	"testing"

	"devshard/transport"

	"github.com/stretchr/testify/require"
)

// stubAdmission stands in for the limiter; only its presence is asserted.
type stubAdmission struct{}

func (stubAdmission) AllowRequest(string, string) error { return nil }

func (stubAdmission) ObserveResult(string, string, int) {}

func (stubAdmission) ObserveTransportFailure(string, string, error) {}

func TestHostClientConfigLeavesCompressionOffByDefault(t *testing.T) {
	config := hostClientConfig(HTTPSessionConfig{}, "/devshard/v2", "gonka1host", nil)

	require.False(t, config.CompressRequestBodies,
		"the write side stays off until the whole roster has the read side")
}

// Anything that stops in this helper never reaches the wire.
func TestHostClientConfigCarriesCompressionToEachHost(t *testing.T) {
	config := hostClientConfig(HTTPSessionConfig{CompressRequestBodies: true}, "/devshard/v2", "gonka1host", nil)

	require.True(t, config.CompressRequestBodies)
}

func TestHostClientConfigCarriesTheRoutePrefixAndParticipant(t *testing.T) {
	config := hostClientConfig(
		HTTPSessionConfig{RequestAdmission: stubAdmission{}},
		"/devshard/v2", "gonka1host", nil,
	)

	require.Equal(t, "/devshard/v2", config.RoutePrefix, "a dropped prefix routes every request into a 404")
	require.Equal(t, "gonka1host", config.ParticipantKey,
		"the admission bucket must key on the same address the rest of the stack uses")
	require.NotNil(t, config.Admission)
}

// Passing a config always works only because the client re-defaults the prefix.
func TestHostClientConfigLeavesAnEmptyPrefixToTheClient(t *testing.T) {
	config := hostClientConfig(HTTPSessionConfig{}, "", "gonka1host", nil)

	require.Empty(t, config.RoutePrefix)
	require.Equal(t, transport.DefaultRoutePrefix(),
		transport.NewHTTPClient("http://127.0.0.1:1", "escrow", nil, config).RoutePrefix())
}
