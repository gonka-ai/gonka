package types

import "os"

// DefaultContractsParams returns default contract parameters including the CW20 code
func DefaultContractsParams() *ContractsParams {
	// Read the CW20 contract code
	wasmCode, err := os.ReadFile("/root/cw20_base.wasm")
	if err != nil {
		panic(err)
	}

	return &ContractsParams{
		Cw20Code:   wasmCode,
		Cw20CodeId: 0, // Default code ID
	}
}
