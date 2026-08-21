package client_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"devshard/chainoracle/blocks"
	blockclient "devshard/chainoracle/blocks/client"

	"github.com/stretchr/testify/require"
)

func TestLookup_AtDummyOn404(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(ts.Close)
	l, err := blockclient.NewLookup(blockclient.HTTPConfig{BaseURL: ts.URL})
	require.NoError(t, err)
	h, err := l.At(context.Background(), 11)
	require.NoError(t, err)
	require.True(t, blocks.IsDummyHeader(h))
	require.Equal(t, int64(11), h.Height)
}

func TestLookup_AtOK(t *testing.T) {
	want := blocks.HashOnlyHeader(4, time.Unix(1, 0).UTC(), "gonka", []byte{9})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/block/4", r.URL.Path)
		_ = json.NewEncoder(w).Encode(want)
	}))
	t.Cleanup(ts.Close)
	l, err := blockclient.NewLookup(blockclient.HTTPConfig{BaseURL: ts.URL})
	require.NoError(t, err)
	h, err := l.At(context.Background(), 4)
	require.NoError(t, err)
	require.Equal(t, int64(4), h.Height)
	require.Equal(t, []byte{9}, h.BlockHash)
}

func TestLookup_LatestUnsupported(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(ts.Close)
	l, err := blockclient.NewLookup(blockclient.HTTPConfig{BaseURL: ts.URL})
	require.NoError(t, err)
	_, err = l.Latest(context.Background())
	require.Error(t, err)
}
