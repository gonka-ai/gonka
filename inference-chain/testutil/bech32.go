package testutil

import (
	"sync"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

var bech32ConfigOnce sync.Once

func EnsureBech32Config() {
	bech32ConfigOnce.Do(func() {
		cfg := sdk.GetConfig()
		cfg.SetBech32PrefixForAccount("gonka", "gonkapub")
		cfg.SetBech32PrefixForValidator("gonkavaloper", "gonkavaloperpub")
		cfg.SetBech32PrefixForConsensusNode("gonkavalcons", "gonkavalconspub")
	})
}
