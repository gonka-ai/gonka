#!/usr/bin/env node
// CLI tool to submit epoch group key to BridgeContract
// Usage: HARDHAT_NETWORK=mainnet node submit-epoch.js <contractAddress> <epochId> <groupPublicKey> [validationSignature]

import hardhat from "hardhat";
import dotenv from "dotenv";
import { base64ToHex, base64SignatureToHex, inspectBLSKey } from "./bls.js";

// Load environment variables
dotenv.config();

// Helper function to detect and validate hex input
function isHexString(str) {
    return typeof str === 'string' && str.startsWith('0x') && /^0x[0-9a-fA-F]*$/.test(str);
}

// Helper function to convert public key (base64 or hex) to hex
function convertPublicKeyToHex(input) {
    if (isHexString(input)) {
        // Already hex - validate length (256 bytes = 512 hex chars + "0x" prefix)
        const hexLength = (input.length - 2) / 2;
        if (hexLength !== 256) {
            throw new Error(`Invalid hex public key length: ${hexLength} bytes. Expected 256 bytes.`);
        }
        return input;
    } else {
        // Assume base64
        return base64ToHex(input);
    }
}

// Helper function to convert signature (base64 or hex) to hex
function convertSignatureToHex(input) {
    if (input === "0x" || input === "" || input === "0") {
        return "0x"; // Genesis epoch
    }
    
    if (isHexString(input)) {
        // Already hex - validate length (128 bytes = 256 hex chars + "0x" prefix)
        const hexLength = (input.length - 2) / 2;
        if (hexLength !== 128) {
            throw new Error(`Invalid hex signature length: ${hexLength} bytes. Expected 128 bytes.`);
        }
        return input;
    } else {
        // Assume base64
        return base64SignatureToHex(input);
    }
}

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

async function submitEpoch(contractAddress, epochId, groupPublicKey, validationSignature) {
    console.log("=== Submit Epoch to Bridge Contract ===\n");
    
    // Get provider and signer
    const { provider, signer, ethers } = await getProviderAndSigner();
    
    // Show network info
    const networkInfo = await provider.getNetwork();
    console.log("Network:", networkInfo.name, `(chainId: ${networkInfo.chainId})`);
    console.log();
    
    // Validate inputs
    if (!contractAddress || !ethers.isAddress(contractAddress)) {
        throw new Error(`Invalid contract address: ${contractAddress}`);
    }
    
    const epochIdNum = parseInt(epochId);
    if (isNaN(epochIdNum) || epochIdNum < 1) {
        throw new Error(`Invalid epoch ID: ${epochId}. Must be a positive integer.`);
    }
    
    console.log("Configuration:");
    console.log("- Contract Address:", contractAddress);
    console.log("- Epoch ID:", epochIdNum);
    console.log();
    
    // Convert group public key (base64 or hex) to hex
    console.log("Converting group public key...");
    const isHexKey = isHexString(groupPublicKey);
    console.log("- Input Format:", isHexKey ? "hex" : "base64");
    
    if (!isHexKey) {
        const keyInfo = inspectBLSKey(groupPublicKey);
        console.log("- Length:", keyInfo.length, "bytes");
        console.log("- Valid:", keyInfo.valid ? "✓" : "✗");
        
        if (!keyInfo.valid) {
            throw new Error("Invalid BLS public key. Expected 256 bytes.");
        }
    }
    
    const hexPublicKey = convertPublicKeyToHex(groupPublicKey);
    console.log("- Length:", (hexPublicKey.length - 2) / 2, "bytes");
    console.log("- Hex:", hexPublicKey.substring(0, 20) + "..." + hexPublicKey.substring(hexPublicKey.length - 10));
    console.log();
    
    // Convert validation signature (base64 or hex) to hex
    console.log("Converting validation signature...");
    const isHexSig = isHexString(validationSignature);
    let hexSignature;
    if (validationSignature === "0x" || validationSignature === "" || validationSignature === "0") {
        // Empty signature for genesis epoch
        hexSignature = "0x";
        console.log("- Using empty signature (genesis epoch)");
    } else {
        console.log("- Input Format:", isHexSig ? "hex" : "base64");
        hexSignature = convertSignatureToHex(validationSignature);
        console.log("- Length:", (hexSignature.length - 2) / 2, "bytes");
        console.log("- Hex:", hexSignature.substring(0, 20) + "..." + hexSignature.substring(hexSignature.length - 10));
    }
    console.log();
    
    // Connect to contract
    console.log("Connecting to contract...");
    const bridge = await ethers.getContractAt("BridgeContract", contractAddress);
    
    // Verify contract exists and is a BridgeContract
    const code = await provider.getCode(contractAddress);
    if (code === "0x") {
        throw new Error(`No contract found at address ${contractAddress}. Please check the address and network.`);
    }
    
    // Check current state
    let currentState;
    try {
        currentState = await bridge.getCurrentState();
    } catch (error) {
        throw new Error(`Contract at ${contractAddress} is not a BridgeContract or is on a different network. Error: ${error.message}`);
    }
    const stateStr = currentState === 0n ? "ADMIN_CONTROL" : "NORMAL_OPERATION";
    console.log("- Current State:", stateStr);
    
    if (currentState !== 0n) {
        throw new Error("Contract must be in ADMIN_CONTROL state to submit epoch keys");
    }
    
    const latestEpoch = await bridge.getLatestEpochInfo();
    console.log("- Latest Epoch ID:", latestEpoch.epochId.toString());
    console.log("- Contract Owner:", await bridge.owner());
    
    console.log("- Your Address:", await signer.getAddress());
    console.log();
    
    // Get current gas fees from the network
    const feeData = await provider.getFeeData();
    console.log("Gas fees:");
    console.log("- Max Fee:", ethers.formatUnits(feeData.maxFeePerGas, "gwei"), "gwei");
    console.log("- Priority Fee:", ethers.formatUnits(feeData.maxPriorityFeePerGas, "gwei"), "gwei");
    console.log();
    
    // Submit epoch using admin function
    console.log(`Submitting epoch ${epochIdNum}...`);
    console.log("(Confirm the transaction on your Ledger if using hardware wallet)");
    const tx = await bridge.setGroupKey(
        epochIdNum,
        hexPublicKey,
        {
            maxFeePerGas: feeData.maxFeePerGas,
            maxPriorityFeePerGas: feeData.maxPriorityFeePerGas,
        }
    );
    
    console.log("✓ Transaction sent:", tx.hash);
    console.log("Waiting for confirmation...");
    
    const receipt = await tx.wait();
    console.log("✓ Transaction confirmed!");
    console.log("- Block Number:", receipt.blockNumber);
    console.log("- Gas Used:", receipt.gasUsed.toString());
    console.log();
    
    // Verify submission
    const newLatestEpoch = await bridge.getLatestEpochInfo();
    console.log("Updated state:");
    console.log("- Latest Epoch ID:", newLatestEpoch.epochId.toString());
    console.log("- Submission Timestamp:", new Date(Number(newLatestEpoch.timestamp) * 1000).toISOString());
    
    // Check if this was the genesis epoch
    if (epochIdNum === 1) {
        console.log("\n✓ Genesis epoch submitted successfully!");
        console.log("\nNext step: Enable normal operation");
        console.log("Run: node enable-bridge.js", contractAddress);
    } else {
        console.log("\n✓ Epoch", epochIdNum, "submitted successfully!");
    }
    
    return { tx, receipt };
}

// Parse command-line arguments
function parseArgs() {
    const args = process.argv.slice(2);
    
    if (args.length < 3) {
        console.error("Usage: HARDHAT_NETWORK=<network> node submit-epoch.js <contractAddress> <epochId> <groupPublicKey> [validationSignature]");
        console.error("\nArguments:");
        console.error("  contractAddress       - Deployed BridgeContract address");
        console.error("  epochId              - Epoch ID (positive integer)");
        console.error("  groupPublicKey       - BLS public key (256 bytes)");
        console.error("                          Format: base64-encoded OR hex (0x-prefixed)");
        console.error("  validationSignature  - BLS signature (128 bytes) or '0x' for genesis");
        console.error("                          Format: base64-encoded OR hex (0x-prefixed)");
        console.error("\nExamples:");
        console.error("  # Genesis epoch (epoch 1) with Ledger on mainnet");
        console.error('  HARDHAT_NETWORK=mainnet node submit-epoch.js 0x1234... 1 "uLyVx3JCS..." 0x');
        console.error("\n  # Subsequent epochs - signature required (base64)");
        console.error('  HARDHAT_NETWORK=mainnet node submit-epoch.js 0x1234... 5 "uLyVx3JCS..." "petZ+65yf..."');
        console.error("\n  # Using hex format on localhost");
        console.error('  HARDHAT_NETWORK=localhost node submit-epoch.js 0x1234... 1 "0xb8bc95c7..." 0x');
        process.exit(1);
    }
    
    return {
        contractAddress: args[0],
        epochId: args[1],
        groupPublicKey: args[2],
        validationSignature: args[3] || "0x" // Default to empty for genesis
    };
}

// Main execution
if (import.meta.url === `file://${process.argv[1]}`) {
    const { contractAddress, epochId, groupPublicKey, validationSignature } = parseArgs();
    
    submitEpoch(contractAddress, epochId, groupPublicKey, validationSignature)
        .then(() => {
            console.log("\n=== Success ===");
            process.exit(0);
        })
        .catch((error) => {
            console.error("\n=== Error ===");
            console.error(error.message);
            if (error.reason) {
                console.error("Reason:", error.reason);
            }
            if (error.code) {
                console.error("Code:", error.code);
            }
            process.exit(1);
        });
}

export {
    submitEpoch
};

