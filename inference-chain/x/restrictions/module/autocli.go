package restrictions

import (
	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"

	modulev1 "github.com/productscience/inference/api/inference/restrictions"
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
					Short:     "Shows the parameters of the restrictions module",
				},
				{
					RpcMethod: "TransferRestrictionStatus",
					Use:       "status",
					Short:     "Query current transfer restriction status",
					Long:      "Query the current status of transfer restrictions including whether they are active, end block, current block, and remaining blocks.",
				},
				{
					RpcMethod: "TransferExemptions",
					Use:       "exemptions",
					Short:     "Query available emergency transfer exemptions",
					Long:      "Query all available emergency transfer exemption templates that can be used during the restriction period.",
				},
				{
					RpcMethod: "ExemptionUsage",
					Use:       "exemption-usage [exemption-id] [account-address]",
					Short:     "Query usage statistics for a specific exemption and account",
					Long:      "Query how many times a specific account has used a particular emergency exemption.",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "exemption_id"},
						{ProtoField: "account_address"},
					},
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
					RpcMethod: "ExecuteEmergencyTransfer",
					Use:       "execute-emergency-transfer [exemption-id] [from-address] [to-address] [amount] [denom]",
					Short:     "Execute an emergency transfer using an approved exemption",
					Long:      "Execute a transfer during the restriction period using a pre-approved emergency exemption template. The transfer must match the exemption parameters exactly.",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "exemption_id"},
						{ProtoField: "from_address"},
						{ProtoField: "to_address"},
						{ProtoField: "amount"},
						{ProtoField: "denom"},
					},
				},
				// this line is used by ignite scaffolding # autocli/tx
			},
		},
	}
}
