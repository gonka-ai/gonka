package com.productscience

import com.productscience.data.*

/**
 * Test mnemonic for deterministic vesting account key generation.
 * This allows us to:
 * 1. Pre-calculate the address for genesis configuration
 * 2. Import the key in tests to sign transactions
 */
const val TEST_VESTING_ACCOUNT_MNEMONIC = "test test test test test test test test test test test junk"

/**
 * Pre-calculated address from TEST_VESTING_ACCOUNT_MNEMONIC.
 * Verified using: echo "test test test test test test test test test test test junk" | inferenced keys add temp --recover --keyring-backend test --output json
 * 
 * Address: gonka1vcqc3gcyu3j937nz7kyvmhwxgqctp88xsq5qlw
 */
const val TEST_VESTING_ACCOUNT_ADDRESS = "gonka1vcqc3gcyu3j937nz7kyvmhwxgqctp88xsq5qlw"

const val TEST_VESTING_ACCOUNT_NAME = "test-vesting-account"

/**
 * Verifies that a vesting account is properly configured in the chain.
 * 
 * @param pair The LocalInferencePair to query
 * @param address The address of the vesting account to verify
 * @return true if the account has both balance and vesting schedule, false otherwise
 */
fun verifyVestingAccount(
    pair: LocalInferencePair, 
    address: String
): Boolean {
    val balance = pair.getBalance(address)
    val vestingResult = runCatching {
        pair.node.queryVestingSchedule(address)
    }
    return balance > 0 && vestingResult.isSuccess && vestingResult.getOrNull()?.vestingSchedule != null
}

