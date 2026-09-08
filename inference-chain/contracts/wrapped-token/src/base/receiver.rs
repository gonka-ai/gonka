use cosmwasm_schema::cw_serde;
use cosmwasm_std::{to_json_binary, Binary, CosmosMsg, StdResult, Uint256, WasmMsg};

#[cw_serde]
pub struct Cw20ReceiveMsg {
    pub sender: String,
    pub amount: Uint256,
    pub msg: Binary,
}

impl Cw20ReceiveMsg {
    pub fn into_json_binary(self) -> StdResult<Binary> {
        let msg = ReceiverExecuteMsg::Receive(self);
        to_json_binary(&msg)
    }

    pub fn into_cosmos_msg<T: Into<String>>(self, contract_addr: T) -> StdResult<CosmosMsg> {
        let msg = self.into_json_binary()?;
        let execute = WasmMsg::Execute {
            contract_addr: contract_addr.into(),
            msg,
            funds: vec![],
        };
        Ok(execute.into())
    }
}

#[cw_serde]
enum ReceiverExecuteMsg {
    Receive(Cw20ReceiveMsg),
}
