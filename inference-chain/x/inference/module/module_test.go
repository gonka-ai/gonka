package inference

import (
	"encoding/base64"
	"fmt"
	"github.com/cometbft/cometbft/crypto/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestName(t *testing.T) {
	keyBytes, err := base64.StdEncoding.DecodeString("WK8uVt3dM4swWiBMAQXeMF12B7FXikK7HgF8GsyttBw=")
	assert.NoError(t, err)

	pk := ed25519.PubKey(keyBytes)
	fmt.Println(sdk.ConsAddress(pk.Address()))
	fmt.Println(sdk.ConsAddress(pk.Address().Bytes()))
	fmt.Println(sdk.ConsAddress(pk.Address().Bytes()).String())
	fmt.Println(pk.Address())
	fmt.Println(pk.Address().String())

}
