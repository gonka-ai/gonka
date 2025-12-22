#!/usr/bin/env node
// CLI tool to enable normal operation mode on BridgeContract
// Usage: HARDHAT_NETWORK=mainnet node enable-normal-operation.js <contractAddress>

import hardhat from "hardhat";
import dotenv from "dotenv";

// Load environment variables
dotenv.config();

// Helper function to get provider and signer (supports Ledger via hardhat-ledger plugin)
async function getProviderAndSigner() {
    // Connect to network and get ethers (Hardhat 3 API)
    const connection = await hardhat.network.connect();
    const { ethers } = connection;
    
    if (!ethers) {
        throw new Error("hardhat-ethers plugin not loaded. Make sure it's in the plugins array in hardhat.config.js");
    }
    
    // Get signers from Hardhat (includes Ledger accounts if configured)
    const signers = await ethers.getSigners();
    if (signers.length === 0) {
        throw new Error("No signers available. Configure accounts in hardhat.config.js or set PRIVATE_KEY/LEDGER_ADDRESS in .env");
    }
    
    const signer = signers[0];
    const provider = ethers.provider;
    return { provider, signer, ethers };
}

async function enableNormalOperation(contractAddress) {
    console.log("=== Enable Normal Operation ===\n");
    
    const { provider, signer, ethers } = await getProviderAndSigner();
    
    // Show network info
    const networkInfo = await provider.getNetwork();
    console.log("Network:", networkInfo.name, `(chainId: ${networkInfo.chainId})`);
    console.log();
    
    // Validate inputs
    if (!contractAddress || !ethers.isAddress(contractAddress)) {
        throw new Error(`Invalid contract address: ${contractAddress}`);
    }
    
    console.log("Contract Address:", contractAddress);
    console.log();
    
    // Connect to contract
    const bridge = await ethers.getContractAt("BridgeContract", contractAddress);
    
    // Verify contract exists
    const code = await provider.getCode(contractAddress);
    if (code === "0x") {
        throw new Error(`No contract found at address ${contractAddress}`);
    }
    
    // Get current state
    console.log("Checking current state...");
    const currentState = await bridge.getCurrentState();
    const stateStr = currentState === 0n ? "ADMIN_CONTROL" : "NORMAL_OPERATION";
    console.log("- Current State:", stateStr);
    
    if (currentState !== 0n) {
        console.log("\n✓ Contract is already in NORMAL_OPERATION mode");
        return { alreadyEnabled: true };
    }
    
    // Check epoch status
    const epochMeta = await bridge.epochMeta();
    const latestEpochId = epochMeta.latestEpochId;
    console.log("- Latest Epoch ID:", latestEpochId.toString());
    
    if (latestEpochId === 0n) {
        throw new Error("Cannot enable normal operation: No genesis epoch has been submitted yet. Submit epoch 1 first.");
    }
    
    // Check ownership
    const owner = await bridge.owner();
    const signerAddress = await signer.getAddress();
    console.log("- Contract Owner:", owner);
    console.log("- Your Address:", signerAddress);
    
    if (owner.toLowerCase() !== signerAddress.toLowerCase()) {
        throw new Error(`Only the contract owner can enable normal operation. Owner: ${owner}`);
    }
    
    console.log();
    
    // Get current gas fees from the network
    const feeData = await provider.getFeeData();
    console.log("Gas fees:");
    console.log("- Max Fee:", ethers.formatUnits(feeData.maxFeePerGas, "gwei"), "gwei");
    console.log("- Priority Fee:", ethers.formatUnits(feeData.maxPriorityFeePerGas, "gwei"), "gwei");
    console.log();
    
    // Enable normal operation
    console.log("Enabling normal operation...");
    console.log("(Confirm the transaction on your Ledger if using hardware wallet)");
    
    try {
        const tx = await bridge.resetToNormalOperation({
            maxFeePerGas: feeData.maxFeePerGas,
            maxPriorityFeePerGas: feeData.maxPriorityFeePerGas,
        });
        
        console.log("✓ Transaction sent:", tx.hash);
        console.log("Waiting for confirmation...");
        
        const receipt = await tx.wait();
        console.log("✓ Transaction confirmed!");
        console.log("- Block Number:", receipt.blockNumber);
        console.log("- Gas Used:", receipt.gasUsed.toString());
        console.log();
        
        // Verify state change
        const newState = await bridge.getCurrentState();
        const newStateStr = newState === 0n ? "ADMIN_CONTROL" : "NORMAL_OPERATION";
        console.log("Updated State:", newStateStr);
        
        if (newState === 1n) {
            console.log("\n✓ Normal operation enabled successfully!");
            console.log("\nThe bridge is now operational and can process:");
            console.log("- Withdrawal commands (ERC-20 tokens)");
            console.log("- Mint commands (WGNK tokens)");
            console.log("- Burn operations (WGNK -> GNK on source chain)");
        } else {
            console.log("\n⚠️  Warning: State did not change to NORMAL_OPERATION");
        }
        
        return { tx, receipt };
    } catch (error) {
        console.error("\n❌ Transaction failed:");
        console.error("Error:", error.message);
        
        // Try to decode the error
        if (error.data) {
            console.error("Error data:", error.data);
            
            // Try to get custom error name
            try {
                const errorData = error.data;
                if (errorData && errorData.startsWith && errorData.startsWith('0x')) {
                    const errorSelector = errorData.slice(0, 10);
                    const errorNames = {
                        '0x6f7c43c8': 'BridgeNotOperational',
                        '0x24d35a26': 'InvalidEpoch',
                        '0xd9a00c27': 'RequestAlreadyProcessed',
                        '0x8baa579f': 'InvalidSignature',
                        '0x80e82c2d': 'MustBeInAdminControl',
                        '0xa42e0c5b': 'InvalidEpochSequence',
                        '0x59c8e5f9': 'NoValidGenesisEpoch',
                        '0x21f3c01d': 'TimeoutNotReached'
                    };
                    if (errorNames[errorSelector]) {
                        console.error(`Custom Error: ${errorNames[errorSelector]}`);
                        
                        if (errorSelector === '0x59c8e5f9') {
                            console.error("\nThis means no genesis epoch has been submitted yet.");
                            console.error("Submit epoch 1 first using: node submit-epoch.js");
                        }
                    }
                }
            } catch (decodeError) {
                // Ignore decode errors
            }
        }
        
        throw error;
    }
}

// Parse command-line arguments
function parseArgs() {
    const args = process.argv.slice(2);
    
    if (args.length < 1) {
        console.error("Usage: HARDHAT_NETWORK=<network> node enable-normal-operation.js <contractAddress>");
        console.error("\nArguments:");
        console.error("  contractAddress  - Deployed BridgeContract address");
        console.error("\nExample:");
        console.error('  HARDHAT_NETWORK=mainnet node enable-normal-operation.js 0x1234...');
        console.error("\nNote:");
        console.error("  - Contract must be in ADMIN_CONTROL state");
        console.error("  - At least one epoch must be submitted");
        console.error("  - Only the contract owner can execute this");
        process.exit(1);
    }
    
    return {
        contractAddress: args[0]
    };
}

// Main execution
if (import.meta.url === `file://${process.argv[1]}`) {
    const { contractAddress } = parseArgs();
    
    enableNormalOperation(contractAddress)
        .then((result) => {
            if (!result.alreadyEnabled) {
                console.log("\n=== Success ===");
                console.log("Bridge is now in NORMAL_OPERATION mode");
            }
            process.exit(0);
        })
        .catch((error) => {
            console.error("\n=== Error ===");
            console.error(error.message);
            if (error.reason) {
                console.error("Reason:", error.reason);
            }
            process.exit(1);
        });
}

export {
    enableNormalOperation
};

