package tipcache

import (
	"bytes"
	"log/slog"
	"testing"

	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/stretchr/testify/require"
)

func TestHandleCometDeliveryWarnsOnBadNewBlock(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	handleCometDelivery(ctypes.ResultEvent{Query: cometNewBlockQuery, Data: "not-a-block"}, &Cache{}, CometHooks{})
	require.Contains(t, buf.String(), "unexpected NewBlock data type")
}
