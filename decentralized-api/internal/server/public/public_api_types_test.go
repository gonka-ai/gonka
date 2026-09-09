package public

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStringOrArray_Unmarshal(t *testing.T) {
	var s StringOrArray

	err := json.Unmarshal([]byte(`"single"`), &s)
	require.NoError(t, err)
	require.Equal(t, StringOrArray{"single"}, s)

	err = json.Unmarshal([]byte(`["a", "b"]`), &s)
	require.NoError(t, err)
	require.Equal(t, StringOrArray{"a", "b"}, s)

	err = json.Unmarshal([]byte(`123`), &s)
	require.Error(t, err)
}
