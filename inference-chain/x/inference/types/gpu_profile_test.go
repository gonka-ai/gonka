package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanonicalGpuProfileId(t *testing.T) {
	node := &HardwareNode{Hardware: []*Hardware{
		{Type: "  nvidia   h100 ", Count: 4},
		{Type: "CPU", Count: 2},
		{Type: "RAM", Count: 512},
		{Type: "NVIDIA H100", Count: 4},
		{Type: "nvme", Count: 2},
		{Type: "", Count: 9},
		{Type: "NVIDIA A100", Count: 1},
	}}
	require.Equal(t, "NVIDIA A100 x1 | NVIDIA H100 x8", CanonicalGpuProfileId(node))
	require.Equal(t, CanonicalGpuProfileId(node), CanonicalGpuProfileIdOf(node.Hardware))
}

func TestCanonicalGpuProfileIdEmpty(t *testing.T) {
	require.Equal(t, "", CanonicalGpuProfileId(&HardwareNode{}))
	require.Equal(t, "", CanonicalGpuProfileId(&HardwareNode{Hardware: []*Hardware{{Type: "CPU", Count: 8}}}))
	require.Equal(t, "", CanonicalGpuProfileIdOf(nil))
}
