// SPDX-License-Identifier: MIT
pragma solidity ^0.8.19;

import "../BridgeContract.sol";

/// @dev Test-only harness that exposes BridgeContract internals so the BLS
/// verification and G2-validity paths can be exercised against the real
/// EIP-2537 precompiles under a Prague EVM. Not for deployment.
contract BridgeHarness is BridgeContract {
    constructor(bytes32 g, bytes32 e) BridgeContract(g, e) {}

    function exposedIsValidG2Point(bytes memory key) external view returns (bool) {
        return _isValidG2Point(key);
    }

    function exposedMapMessageToG1(bytes32 messageHash) external view returns (bytes memory) {
        return _mapMessageToG1(messageHash);
    }

    /// @dev Raw BLS check against an arbitrary message hash, with no domain tag.
    function exposedVerifyRaw(
        bytes memory key,
        bytes32 messageHash,
        bytes memory sig
    ) external view returns (bool) {
        return _verifyBLSSignature(key, messageHash, sig);
    }

    function exposedVerifyTransition(
        bytes memory prevKey,
        bytes memory newKey,
        bytes memory sig,
        uint64 prevEpoch
    ) external view returns (bool) {
        return _verifyTransitionSignature(
            _bytesToGroupKey(prevKey),
            _bytesToGroupKey(newKey),
            sig,
            prevEpoch
        );
    }

    /// @dev Reproduces the exact pre-image the contract hashes for a rotation,
    /// so JS test code signs the identical bytes the verifier checks.
    function transitionMessageHash(bytes memory newKey, uint64 prevEpoch)
        external
        view
        returns (bytes32)
    {
        GroupKey memory k = _bytesToGroupKey(newKey);
        return keccak256(
            abi.encodePacked(
                prevEpoch,
                GONKA_CHAIN_ID,
                TRANSITION_OPERATION,
                k.part0, k.part1, k.part2, k.part3,
                k.part4, k.part5, k.part6, k.part7
            )
        );
    }
}
