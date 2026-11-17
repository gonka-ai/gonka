// SPDX-License-Identifier: MIT
pragma solidity ^0.8.19;

import "../BridgeContract.sol";

// Expose arithmetic only. Redemption tests use the unmodified production
// BridgeContract and the EVM's real EIP-2537 precompiles.
contract BridgeContractHarness is BridgeContract {
    constructor() BridgeContract(bytes32(uint256(1)), bytes32(uint256(2))) {}

    function negateG1(bytes memory point) external pure returns (bytes memory) {
        return _negateG1(point);
    }
}

contract BridgeTestToken is ERC20 {
    constructor(address holder, uint256 amount) ERC20("Bridge test token", "BTT") {
        _mint(holder, amount);
    }
}
