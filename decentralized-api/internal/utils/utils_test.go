package utils

import (
	"context"
	cosmos_client "decentralized-api/cosmosclient"
	"fmt"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestQueryParticipants(t *testing.T) {
	rpcClient, err := cosmos_client.NewRpcClient("http://localhost:26657")
	assert.NoError(t, err)

	particiapnts, err := QueryActiveParticipants(rpcClient, 1)(context.Background(), "")
	assert.NoError(t, err)
	fmt.Println(particiapnts)
}
