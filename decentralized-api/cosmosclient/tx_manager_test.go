package cosmosclient

import (
	"decentralized-api/apiconfig"
	testutil "github.com/productscience/inference/testutil/cosmoclient"
	"github.com/stretchr/testify/assert"
	"testing"
)

var testNatsConfig = apiconfig.NatsServerConfig{
	Host:     "localhost",
	Port:     4222,
	TestMode: true,
}

func TestTxManager(t *testing.T) {
	const (
		network = "cosmos"

		accountName = "cosmosaccount"
		mnemonic    = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
		passphrase  = "testpass"
	)
	client := testutil.NewMockClient(t, network, accountName, mnemonic, passphrase)

	account, err := client.AccountRegistry.GetByName(accountName)
	assert.NoError(t, err)

	addr, err := account.Address(network)
	assert.NoError(t, err)

	NewTxManager(&client, &account, addr)
}
