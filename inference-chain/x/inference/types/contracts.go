package types

import "os"

// GetDefaultCW20ContractsParams returns default contract parameters including our custom wrapped-token contract
func GetDefaultCW20ContractsParams() *CosmWasmParams {
	wasmCode, err := os.ReadFile("/root/wrapped_token.wasm")
	if err != nil {
		wasmCode, err = os.ReadFile("/root/.inference/cosmovisor/current/wrapped_token.wasm")
		if err != nil {
			panic(err)
		}
	}

	return &CosmWasmParams{
		Cw20Code:   wasmCode,
		Cw20CodeId: 0, // Default code ID
	}
}
