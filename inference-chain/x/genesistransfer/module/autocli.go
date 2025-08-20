package genesistransfer

import (
	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"

	modulev1 "github.com/productscience/inference/api/inference/genesistransfer"
)

// AutoCLIOptions implements the autocli.HasAutoCLIConfig interface.
func (am AppModule) AutoCLIOptions() *autocliv1.ModuleOptions {
	return &autocliv1.ModuleOptions{
		Query: &autocliv1.ServiceCommandDescriptor{
			Service: modulev1.Query_ServiceDesc.ServiceName,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{
					RpcMethod: "Params",
					Use:       "params",
					Short:     "Shows the parameters of the module",
				},
				{
					RpcMethod:      "TransferStatus",
					Use:            "transfer-status [genesis-address]",
					Short:          "Shows the transfer status for a genesis account",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "genesis_address"}},
				},
				{
					RpcMethod: "TransferHistory",
					Use:       "transfer-history",
					Short:     "List all transfer records",
				},
				{
					RpcMethod:      "TransferEligibility",
					Use:            "transfer-eligibility [genesis-address]",
					Short:          "Check if a genesis account is eligible for transfer",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "genesis_address"}},
				},
				{
					RpcMethod: "AllowedAccounts",
					Use:       "allowed-accounts",
					Short:     "List all accounts allowed for transfer",
				},
				// this line is used by ignite scaffolding # autocli/query
			},
		},
		Tx: &autocliv1.ServiceCommandDescriptor{
			Service:              modulev1.Msg_ServiceDesc.ServiceName,
			EnhanceCustomCommand: true, // only required if you want to use the custom command
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{
					RpcMethod: "UpdateParams",
					Skip:      true, // skipped because authority gated
				},
				{
					RpcMethod: "TransferOwnership",
					Use:       "transfer-ownership [genesis-address] [recipient-address]",
					Short:     "Transfer ownership of a genesis account to a recipient",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "genesis_address"},
						{ProtoField: "recipient_address"},
					},
				},
				// this line is used by ignite scaffolding # autocli/tx
			},
		},
	}
}
