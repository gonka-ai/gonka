package types

import "os"

// GetDefaultCW20ContractsParams returns default contract parameters including the CW20 code
func GetDefaultCW20ContractsParams() *CosmWasmParams {
	// Read the CW20 contract code
	wasmCode, err := os.ReadFile("/root/cw20_base.wasm")
	if err != nil {
		wasmCode, err = os.ReadFile("/root/.inference/cosmovisor/current/cw20_base.wasm")
		if err != nil {
			panic(err)
		}
	}

	return &CosmWasmParams{
		Cw20Code:   wasmCode,
		Cw20CodeId: 0, // Default code ID
	}
}
