package streamvesting

import (
	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"

	modulev1 "github.com/productscience/inference/api/inference/streamvesting"
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
					RpcMethod: "VestingSchedule",
					Use:       "vesting-schedule [participant-address]",
					Short:     "Shows the full vesting schedule for a participant",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "participant_address"},
					},
				},
				{
					RpcMethod: "TotalVestingAmount",
					Use:       "total-vesting [participant-address]",
					Short:     "Shows the total vesting amount for a participant",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "participant_address"},
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
				// this line is used by ignite scaffolding # autocli/tx
			},
		},
	}
}
