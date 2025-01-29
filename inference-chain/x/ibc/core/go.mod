module github.com/cosmos/ibc-go/v8/modules/core

go 1.23.1

replace (
	github.com/cosmos/ibc-go/api => ../../api
	github.com/cosmos/ibc-go/modules/capability => ../capability
	github.com/productscience/inference => ../../..
)

require (
	cosmossdk.io/core v0.11.1
	cosmossdk.io/depinject v1.1.0
	github.com/cosmos/cosmos-sdk v0.50.11
	github.com/cosmos/ibc-go/modules/capability v0.0.0-00010101000000-000000000000
	github.com/productscience/inference v0.0.0-00010101000000-000000000000
) 