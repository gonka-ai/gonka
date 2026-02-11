#!/usr/bin/env node
// CLI tool to get BridgeContract state and submitted epochs
// Usage: node get-contract-state.js <contractAddress>

import hre from "hardhat";
import { ethers } from "ethers";
import dotenv from "dotenv";

// Load environment variables
dotenv.config();

// Helper function to get provider
async function getProvider() {
    const networkConnection = await hre.network.connect();
    const networkName = networkConnection.networkName;
    
    let rpcUrl;
    if (networkName === "localhost" || networkName === "hardhat") {
        rpcUrl = "http://127.0.0.1:8545";
    } else {
        rpcUrl = process.env[`${networkName.toUpperCase()}_RPC_URL`];
        if (!rpcUrl) throw new Error(`RPC URL not found for network ${networkName}`);
    }
    
    const provider = new ethers.JsonRpcProvider(rpcUrl);
    return { provider, ethers };
}

async function getContractState(contractAddress, showFull = false) {
    console.log("=== Bridge Contract State ===\n");
    
    const { provider, ethers } = await getProvider();
    
    // Show network info
    const network = await provider.getNetwork();
    console.log("Network:", network.name, `(chainId: ${network.chainId})`);
    console.log();
    
    // Validate inputs
    if (!contractAddress || !ethers.isAddress(contractAddress)) {
        throw new Error(`Invalid contract address: ${contractAddress}`);
    }
    
    console.log("Contract Address:", contractAddress);
    console.log();
    
    // Connect to contract (read-only, no signer needed)
    const artifact = await hre.artifacts.readArtifact("BridgeContract");
    const bridge = new ethers.Contract(contractAddress, artifact.abi, provider);
    
    // Verify contract exists
    const code = await provider.getCode(contractAddress);
    if (code === "0x") {
        throw new Error(`No contract found at address ${contractAddress}`);
    }
    
    // Get current state
    console.log("=== Contract State ===");
    const currentState = await bridge.getCurrentState();
    const stateStr = currentState === 0n ? "ADMIN_CONTROL" : "NORMAL_OPERATION";
    console.log("Current State:", stateStr);
    console.log();
    
    // Get epoch metadata
    const epochMeta = await bridge.epochMeta();
    const latestEpochId = epochMeta.latestEpochId;
    const submissionTimestamp = epochMeta.submissionTimestamp;
    
    console.log("=== Epoch Metadata ===");
    console.log("Latest Epoch ID:", latestEpochId.toString());
    console.log("Last Submission:", new Date(Number(submissionTimestamp) * 1000).toISOString());
    console.log();
    
    // Get contract owner
    const owner = await bridge.owner();
    console.log("=== Admin Info ===");
    console.log("Contract Owner:", owner);
    console.log();
    
    // Get constants
    const maxStoredEpochs = await bridge.MAX_STORED_EPOCHS();
    const timeoutDuration = await bridge.TIMEOUT_DURATION();
    const gonkaChainId = await bridge.GONKA_CHAIN_ID();
    const ethereumChainId = await bridge.ETHEREUM_CHAIN_ID();
    
    console.log("=== Configuration ===");
    console.log("Max Stored Epochs:", maxStoredEpochs.toString());
    console.log("Timeout Duration:", timeoutDuration.toString(), "seconds");
    console.log("Gonka Chain ID:", gonkaChainId);
    console.log("Ethereum Chain ID:", ethereumChainId);
    console.log();
    
    // Get all submitted epochs
    console.log("=== Submitted Epochs ===");
    
    if (latestEpochId === 0n) {
        console.log("No epochs submitted yet.");
    } else {
        // Calculate range of epochs to check
        const startEpoch = latestEpochId > maxStoredEpochs 
            ? latestEpochId - maxStoredEpochs + 1n 
            : latestEpochId - 10n;
        
        console.log(`Checking epochs ${startEpoch} to ${latestEpochId}...\n`);
        
        const epochs = [];
        
        // Query each epoch
        for (let epochId = startEpoch; epochId <= latestEpochId; epochId++) {
            try {
                const groupKey = await bridge.epochGroupKeys(epochId);
                
                // Check if epoch has a key (not all zeros)
                const isEmpty = groupKey.part0 === ethers.ZeroHash && 
                               groupKey.part1 === ethers.ZeroHash && 
                               groupKey.part2 === ethers.ZeroHash &&
                               groupKey.part3 === ethers.ZeroHash &&
                               groupKey.part4 === ethers.ZeroHash &&
                               groupKey.part5 === ethers.ZeroHash &&
                               groupKey.part6 === ethers.ZeroHash &&
                               groupKey.part7 === ethers.ZeroHash;
                
                if (!isEmpty) {
                    // Convert GroupKey struct to bytes (8 x 32 bytes = 256 bytes)
                    const keyBytes = ethers.concat([
                        groupKey.part0,
                        groupKey.part1,
                        groupKey.part2,
                        groupKey.part3,
                        groupKey.part4,
                        groupKey.part5,
                        groupKey.part6,
                        groupKey.part7
                    ]);
                    
                    const fullKey = ethers.hexlify(keyBytes);
                    epochs.push({
                        epochId: epochId.toString(),
                        groupPublicKey: fullKey,
                        groupPublicKeyShort: fullKey.substring(0, 20) + "..." + 
                                           fullKey.substring(fullKey.length - 10)
                    });
                }
            } catch (error) {
                console.log(`  Epoch ${epochId}: Error reading (${error.message})`);
            }
        }
        
        if (epochs.length === 0) {
            console.log("No valid epochs found in storage.");
        } else {
            console.log(`Found ${epochs.length} epoch(s):\n`);
            
            epochs.forEach(epoch => {
                console.log(`Epoch ID: ${epoch.epochId}`);
                const keyToDisplay = showFull ? epoch.groupPublicKey : epoch.groupPublicKeyShort;
                console.log(`  Group Public Key: ${keyToDisplay}`);
                console.log();
            });
            
            // Show full details option only if not already showing full
            if (!showFull) {
                console.log("=== Full Epoch Details ===");
                console.log("Use --full flag to see complete public keys");
            }
        }
    }
    
    return {
        currentState: stateStr,
        latestEpochId: latestEpochId.toString(),
        owner,
        network: network.name
    };
}

// Parse command-line arguments
function parseArgs() {
    const args = process.argv.slice(2);
    
    if (args.length < 1) {
        console.error("Usage: node get-contract-state.js <contractAddress> [--full]");
        console.error("\nArguments:");
        console.error("  contractAddress  - Deployed BridgeContract address");
        console.error("  --full           - Show complete public keys (optional)");
        console.error("\nExample:");
        console.error('  node get-contract-state.js 0x1c3566B055f4F0ff49603152457cc88e0C1100D2');
        console.error('  node get-contract-state.js 0x1c3566B055f4F0ff49603152457cc88e0C1100D2 --full');
        process.exit(1);
    }
    
    return {
        contractAddress: args[0],
        showFull: args.includes('--full')
    };
}

// Main execution
if (import.meta.url === `file://${process.argv[1]}`) {
    const { contractAddress, showFull } = parseArgs();
    
    getContractState(contractAddress, showFull)
        .then((state) => {
            console.log("\n=== Summary ===");
            console.log(`State: ${state.currentState}`);
            console.log(`Latest Epoch: ${state.latestEpochId}`);
            console.log(`Owner: ${state.owner}`);
            console.log("\n✓ Query completed successfully");
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
    getContractState
};

