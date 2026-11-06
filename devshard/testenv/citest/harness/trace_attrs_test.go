package harness

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAttrQueryToJaegerTags_TraceQL(t *testing.T) {
	tags, err := attrQueryToJaegerTags(`{ span.devshard.disposition = "ghost" && span.model = "llama" }`)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"devshard.disposition": "ghost",
		"model":                "llama",
	}, tags)
}

func TestAttrQueryToJaegerTags_Logfmt(t *testing.T) {
	tags, err := attrQueryToJaegerTags(`devshard.disposition=ghost service.name=devshardctl`)
	require.NoError(t, err)
	require.Equal(t, "ghost", tags["devshard.disposition"])
	require.Equal(t, "devshardctl", tags["service.name"])
}

func TestLooksLikeTraceQL(t *testing.T) {
	require.True(t, looksLikeTraceQL(`{ span.devshard.disposition = "ghost" }`))
	require.False(t, looksLikeTraceQL(`devshard.disposition=ghost`))
}
