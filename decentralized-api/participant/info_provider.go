package participant

import (
	"decentralized-api/cosmosclient"
	"github.com/productscience/inference/x/inference/utils"
)

type CurrenParticipantInfo interface {
	GetAddress() string
	GetPubKey() string
}

type CosmosInfo struct {
	Address string
	PubKey  string
}

func NewCurrentParticipantInfo(client cosmosclient.CosmosMessageClient) (*CosmosInfo, error) {
	address := client.GetAddress()

	pubKey, err := client.GetAccount().Record.GetPubKey()
	if err != nil {
		// Handle the error appropriately, maybe log it or return an empty string
		return nil, err
	}
	pubkeyString := utils.PubKeyToHexString(pubKey)

	return &CosmosInfo{Address: address, PubKey: pubkeyString}, nil
}

func (c CosmosInfo) GetAddress() string {
	return c.Address
}

func (c CosmosInfo) GetPubKey() string {
	return c.PubKey
}
