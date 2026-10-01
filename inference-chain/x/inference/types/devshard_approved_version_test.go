package types_test

import (
	"strings"
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestDevshardApprovedVersion_Validate(t *testing.T) {
	ok := types.DevshardApprovedVersion{
		Name:   "v1",
		Binary: "https://example.com/devshardd.zip",
		Sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	require.NoError(t, ok.Validate())

	empty := ok
	empty.Name = ""
	require.ErrorContains(t, empty.Validate(), "name cannot be empty")

	longName := ok
	longName.Name = strings.Repeat("n", types.MaxDevshardApprovedVersionNameLen+1)
	require.ErrorContains(t, longName.Validate(), "name exceeds")

	for _, name := range []string{".", "..", " v2 ", "../escape", "nested/v2", "/abs", `foo\bar`} {
		bad := ok
		bad.Name = name
		require.Error(t, bad.Validate(), "name %q", name)
		require.Error(t, types.ValidateApprovedVersionName(name), "name %q", name)
	}
	require.NoError(t, types.ValidateApprovedVersionName("v4.1"))
	require.NoError(t, types.ValidateApprovedVersionName("v4-r2"))

	longURL := ok
	longURL.Binary = strings.Repeat("u", types.MaxDevshardApprovedVersionBinaryLen+1)
	require.ErrorContains(t, longURL.Validate(), "binary exceeds")

	badHex := ok
	badHex.Sha256 = strings.Repeat("g", 64)
	require.ErrorContains(t, badHex.Validate(), "not valid hex")
}

func TestDevshardEscrowParams_Validate_RejectsDeprecatedApprovedVersions(t *testing.T) {
	p := types.DefaultDevshardEscrowParams()
	p.ApprovedVersions = []*types.DevshardApprovedVersion{{
		Name:   "v1",
		Binary: "https://example.com/x.zip",
		Sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}}
	require.ErrorContains(t, p.Validate(), "deprecated")
}
