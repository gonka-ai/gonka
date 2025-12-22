#!/usr/bin/env node
// CLI tool to mint WGNK tokens using mintWithSignature function
// Usage: node mint-wgnk.js <contractAddress> <epochId> <requestId> <recipient> <amount> <signature>

import hre from "hardhat";
import { ethers } from "ethers";
import dotenv from "dotenv";
import { base64SignatureToHex } from "./bls.js";

// Load environment variables
dotenv.config();

// Helper function to convert base64 requestId to hex (32 bytes)
function convertRequestIdToHex(base64RequestId) {
    const cleanInput = base64RequestId.trim();
    
    // Decode base64 to Buffer
    const buffer = Buffer.from(cleanInput, 'base64');
    
    // Verify it's exactly 32 bytes (bytes32)
    if (buffer.length !== 32) {
        throw new Error(
            `Invalid requestId length: expected 32 bytes, got ${buffer.length} bytes. ` +
            `Base64 input: "${cleanInput}"`
        );
    }
    
    // Convert to hex with 0x prefix
    return '0x' + buffer.toString('hex');
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

async function mintWGNK(contractAddress, epochId, requestId, recipient, amount, signature) {
    console.log("=== Mint WGNK Tokens ===\n");
    
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
    
    if (!recipient || !ethers.isAddress(recipient)) {
        throw new Error(`Invalid recipient address: ${recipient}`);
    }
    
    // Validate and parse amount
    let amountBigInt;
    try {
        amountBigInt = BigInt(amount);
        if (amountBigInt <= 0n) {
            throw new Error("Amount must be positive");
        }
    } catch (error) {
        throw new Error(`Invalid amount: ${amount}. Must be a positive integer in wei.`);
    }
    
    console.log("Configuration:");
    console.log("- Contract Address:", contractAddress);
    console.log("- Epoch ID:", epochIdNum);
    console.log("- Recipient:", recipient);
    console.log("- Amount:", amountBigInt.toString(), "wei");
    console.log();
    
    // Convert requestId from base64 to hex
    console.log("Converting request ID...");
    console.log("- Input Format: base64");
    let hexRequestId;
    try {
        hexRequestId = convertRequestIdToHex(requestId);
        console.log("- Length:", (hexRequestId.length - 2) / 2, "bytes");
        console.log("- Hex:", hexRequestId);
    } catch (error) {
        throw new Error(`Failed to convert requestId: ${error.message}`);
    }
    console.log();
    
    // Convert signature from base64 to hex
    console.log("Converting signature...");
    console.log("- Input Format: base64");
    let hexSignature;
    try {
        hexSignature = base64SignatureToHex(signature);
        console.log("- Length:", (hexSignature.length - 2) / 2, "bytes");
        console.log("- Hex:", hexSignature.substring(0, 20) + "..." + hexSignature.substring(hexSignature.length - 10));
    } catch (error) {
        throw new Error(`Failed to convert signature: ${error.message}`);
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
    
    const latestEpoch = await bridge.getLatestEpochInfo();
    console.log("- Latest Epoch ID:", latestEpoch.epochId.toString());
    console.log("- Your Address:", await signer.getAddress());
    console.log();
    
    // Build MintCommand struct
    const mintCommand = {
        epochId: epochIdNum,
        requestId: hexRequestId,
        recipient: recipient,
        amount: amountBigInt,
        signature: hexSignature
    };
    
    // Estimate gas first to catch errors
    console.log(`Estimating gas for mint request...`);
    try {
        const gasEstimate = await bridge.mintWithSignature.estimateGas(mintCommand);
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
                        '0x24d35a26': 'InvalidEpoch (epoch not found or invalid)',
                        '0xd9a00c27': 'RequestAlreadyProcessed (requestId already used)',
                        '0x8baa579f': 'InvalidSignature (BLS signature verification failed)',
                        '0x80e82c2d': 'MustBeInAdminControl',
                        '0xa42e0c5b': 'InvalidEpochSequence',
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
    
    // Execute mint
    console.log(`Minting WGNK tokens...`);
    const tx = await bridge.mintWithSignature(mintCommand);
    
    console.log("✓ Transaction sent:", tx.hash);
    console.log("Waiting for confirmation...");
    
    const receipt = await tx.wait();
    console.log("✓ Transaction confirmed!");
    console.log("- Block Number:", receipt.blockNumber);
    console.log("- Gas Used:", receipt.gasUsed.toString());
    console.log();
    
    // Check for WGNKMinted event
    const mintedEvent = receipt.logs.find(log => {
        try {
            const parsed = bridge.interface.parseLog(log);
            return parsed && parsed.name === 'WGNKMinted';
        } catch {
            return false;
        }
    });
    
    if (mintedEvent) {
        const parsed = bridge.interface.parseLog(mintedEvent);
        console.log("Mint Details:");
        console.log("- Epoch ID:", parsed.args.epochId.toString());
        console.log("- Request ID:", parsed.args.requestId);
        console.log("- Recipient:", parsed.args.recipient);
        console.log("- Amount:", parsed.args.amount.toString(), "wei");
    }
    
    // Get updated WGNK balance
    const balance = await bridge.balanceOf(recipient);
    console.log("\nRecipient WGNK Balance:", balance.toString(), "wei");
    
    console.log("\n✓ WGNK tokens minted successfully!");
    
    return { tx, receipt };
}

// Parse command-line arguments
function parseArgs() {
    const args = process.argv.slice(2);
    
    if (args.length < 6) {
        console.error("Usage: node mint-wgnk.js <contractAddress> <epochId> <requestId> <recipient> <amount> <signature>");
        console.error("\nArguments:");
        console.error("  contractAddress  - Deployed BridgeContract address");
        console.error("  epochId          - Epoch ID for signature validation");
        console.error("  requestId        - Unique request ID from source chain (base64, 32 bytes)");
        console.error("  recipient        - Ethereum address to receive WGNK");
        console.error("  amount           - Amount of WGNK to mint (in wei)");
        console.error("  signature        - BLS threshold signature (base64, 128 bytes)");
        console.error("\nRequirements:");
        console.error("  - Contract must be in NORMAL_OPERATION mode");
        console.error("  - RequestId must not have been processed before");
        console.error("  - Signature must be valid for the mint message");
        console.error("\nExamples:");
        console.error("  # Mint 1 WGNK (1e18 wei)");
        console.error('  node mint-wgnk.js 0x1234... 6 "oOqwXaxnAiS..." 0x5678... 1000000000000000000 "AAAAAAAAABIXIy..."');
        console.error("\n  # Mint 100 WGNK");
        console.error('  node mint-wgnk.js 0x1234... 6 "oOqwXaxnAiS..." 0x5678... 100000000000000000000 "AAAAAAAABIX..."');
        console.error("\nNote:");
        console.error("  - requestId and signature must be in base64 format");
        console.error("  - amount is in wei (1 WGNK = 1e18 wei)");
        console.error("  - 1 WGNK = 1000000000000000000 wei");
        process.exit(1);
    }
    
    return {
        contractAddress: args[0],
        epochId: args[1],
        requestId: args[2],
        recipient: args[3],
        amount: args[4],
        signature: args[5]
    };
}

// Main execution
if (import.meta.url === `file://${process.argv[1]}`) {
    const { contractAddress, epochId, requestId, recipient, amount, signature } = parseArgs();
    
    mintWGNK(contractAddress, epochId, requestId, recipient, amount, signature)
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
    mintWGNK
};

