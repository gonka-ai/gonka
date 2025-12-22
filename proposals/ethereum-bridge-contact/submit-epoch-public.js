#!/usr/bin/env node
// CLI tool to submit epoch group key using public submitGroupKey function
// This is for NORMAL_OPERATION mode (non-admin submissions)
// Usage: node submit-epoch-public.js <contractAddress> <epochId> <groupPublicKey> <validationSignature>

import hre from "hardhat";
import { ethers } from "ethers";
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

// Helper function to get provider and signer
async function getProviderAndSigner() {
    const networkConnection = await hre.network.connect();
    const networkName = networkConnection.networkName;
    
    let rpcUrl;
    let signer;
    
    if (networkName === "localhost" || networkName === "hardhat") {
        rpcUrl = "http://127.0.0.1:8545";
        const provider = new ethers.JsonRpcProvider(rpcUrl);
        signer = await provider.getSigner();
        return { provider, signer, ethers };
    } else {
        // Remote network - use private key from env
        rpcUrl = process.env[`${networkName.toUpperCase()}_RPC_URL`];
        if (!rpcUrl) {
            throw new Error(`RPC URL not found for network ${networkName}. Set ${networkName.toUpperCase()}_RPC_URL in your .env file.`);
        }
        
        const privateKey = process.env.PRIVATE_KEY;
        if (!privateKey) {
            throw new Error(`PRIVATE_KEY not found in environment. Set PRIVATE_KEY in your .env file.`);
        }
        
        const provider = new ethers.JsonRpcProvider(rpcUrl);
        signer = new ethers.Wallet(privateKey, provider);
        return { provider, signer, ethers };
    }
}

async function submitEpochPublic(contractAddress, epochId, groupPublicKey, validationSignature) {
    console.log("=== Submit Epoch (Public) to Bridge Contract ===\n");
    
    const { provider, signer, ethers } = await getProviderAndSigner();
    
    // Show network info
    const network = await provider.getNetwork();
    console.log("Network:", network.name, `(chainId: ${network.chainId})`);
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
    const artifact = await hre.artifacts.readArtifact("BridgeContract");
    const bridge = new ethers.Contract(contractAddress, artifact.abi, signer);
    
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
    
    if (currentState === 0n) {
        console.log("\n⚠️  Warning: Contract is in ADMIN_CONTROL mode");
        console.log("The public submitGroupKey function requires NORMAL_OPERATION mode.");
        console.log("\nTo use in ADMIN_CONTROL mode, use:");
        console.log("  node submit-epoch.js (admin only)");
        console.log("\nTo enable NORMAL_OPERATION mode, use:");
        console.log("  node enable-normal-operation.js");
        throw new Error("Contract must be in NORMAL_OPERATION mode for public submissions");
    }
    
    const latestEpoch = await bridge.getLatestEpochInfo();
    console.log("- Latest Epoch ID:", latestEpoch.epochId.toString());
    
    // Verify sequential submission
    const expectedEpochId = Number(latestEpoch.epochId) + 1;
    if (epochIdNum !== expectedEpochId) {
        console.log(`\n⚠️  Warning: Non-sequential epoch submission`);
        console.log(`Expected epoch ID: ${expectedEpochId}`);
        console.log(`Provided epoch ID: ${epochIdNum}`);
        console.log("\nThe public submitGroupKey function requires sequential submission.");
        console.log("You must submit epochs in order: 1, 2, 3, 4, ...");
        throw new Error(`Invalid epoch sequence. Expected ${expectedEpochId}, got ${epochIdNum}`);
    }
    
    console.log("- Your Address:", await signer.getAddress());
    console.log();
    
    // Estimate gas first to catch errors
    console.log(`Estimating gas for epoch ${epochIdNum}...`);
    try {
        const gasEstimate = await bridge.submitGroupKey.estimateGas(
            epochIdNum,
            hexPublicKey,
            hexSignature
        );
        console.log("- Estimated Gas:", gasEstimate.toString());
    } catch (estimateError) {
        console.error("\n❌ Transaction simulation failed:");
        console.error("Error:", estimateError.message);
        
        // Try to decode the error
        if (estimateError.data) {
            console.error("Error data:", estimateError.data);
            
            // Try to get custom error name
            try {
                const errorData = estimateError.data;
                if (errorData && errorData.startsWith && errorData.startsWith('0x')) {
                    const errorSelector = errorData.slice(0, 10);
                    const errorNames = {
                        '0x6f7c43c8': 'BridgeNotOperational (contract not in NORMAL_OPERATION)',
                        '0x24d35a26': 'InvalidEpoch',
                        '0xd9a00c27': 'RequestAlreadyProcessed',
                        '0x8baa579f': 'InvalidSignature (validation signature verification failed)',
                        '0x80e82c2d': 'MustBeInAdminControl',
                        '0xa42e0c5b': 'InvalidEpochSequence (must submit epochs sequentially)',
                        '0x59c8e5f9': 'NoValidGenesisEpoch',
                        '0x21f3c01d': 'TimeoutNotReached'
                    };
                    if (errorNames[errorSelector]) {
                        console.error(`\nCustom Error: ${errorNames[errorSelector]}`);
                    }
                }
            } catch (decodeError) {
                // Ignore decode errors
            }
        }
        
        throw estimateError;
    }
    console.log();
    
    // Submit epoch
    console.log(`Submitting epoch ${epochIdNum}...`);
    const tx = await bridge.submitGroupKey(
        epochIdNum,
        hexPublicKey,
        hexSignature
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
    
    console.log("\n✓ Epoch", epochIdNum, "submitted successfully!");
    
    return { tx, receipt };
}

// Parse command-line arguments
function parseArgs() {
    const args = process.argv.slice(2);
    
    if (args.length < 3) {
        console.error("Usage: node submit-epoch-public.js <contractAddress> <epochId> <groupPublicKey> [validationSignature]");
        console.error("\nArguments:");
        console.error("  contractAddress       - Deployed BridgeContract address");
        console.error("  epochId              - Epoch ID (must be sequential: latestEpoch + 1)");
        console.error("  groupPublicKey       - BLS public key (256 bytes)");
        console.error("                          Format: base64-encoded OR hex (0x-prefixed)");
        console.error("  validationSignature  - BLS signature (128 bytes) from previous epoch");
        console.error("                          Format: base64-encoded OR hex (0x-prefixed)");
        console.error("\nRequirements:");
        console.error("  - Contract must be in NORMAL_OPERATION mode");
        console.error("  - Epochs must be submitted sequentially (no gaps)");
        console.error("  - Anyone can submit (no admin required)");
        console.error("\nExamples:");
        console.error("  # Using base64 format");
        console.error('  node submit-epoch-public.js 0x1234... 6 "uLyVx3JCS..." "petZ+65yf..."');
        console.error("\n  # Using hex format");
        console.error('  node submit-epoch-public.js 0x1234... 6 "0xb8bc95c7..." "0xa9b599fb..."');
        console.error("\nNote:");
        console.error("  For admin submissions in ADMIN_CONTROL mode, use:");
        console.error("  node submit-epoch.js (allows non-sequential epochs)");
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
    
    submitEpochPublic(contractAddress, epochId, groupPublicKey, validationSignature)
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
    submitEpochPublic
};

