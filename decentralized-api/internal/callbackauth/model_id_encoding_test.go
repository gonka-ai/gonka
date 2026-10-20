package callbackauth

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEncodeModelIDSurvivesPathNormalization(t *testing.T) {
	for _, modelID := range []string{".", "..", "g", "v", "org/model", "a%b", "a+b", "with space", "模型/雪"} {
		t.Run(modelID, func(t *testing.T) {
			callbackURL := "http://api/v2/poc-batches/" + EncodeModelID(modelID)
			normalized, err := url.JoinPath(callbackURL, "v", "200", "200", "token", "validated")
			require.NoError(t, err)
			require.Equal(t, callbackURL+"/v/200/200/token/validated", normalized)
			parsed, err := url.Parse(callbackURL)
			require.NoError(t, err)
			decoded, err := url.PathUnescape(parsed.Path[len("/v2/poc-batches/"):])
			require.NoError(t, err)
			require.Equal(t, modelID, decoded)
		})
	}
}
