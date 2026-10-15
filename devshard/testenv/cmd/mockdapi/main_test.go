package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseMLNodes(t *testing.T) {
	nodes := parseMLNodes("mock-openai-0=http://mock-openai-0:8088, mock-openai-1=http://mock-openai-1:8088")
	require.Equal(t, []string{"mock-openai-0", "mock-openai-1"}, []string{nodes[0].ID, nodes[1].ID})
	require.Equal(t, []string{"http://mock-openai-0:8088", "http://mock-openai-1:8088"}, []string{nodes[0].Endpoint, nodes[1].Endpoint})
}
