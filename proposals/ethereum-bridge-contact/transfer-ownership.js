#!/usr/bin/env node
// CLI tool to transfer ownership of BridgeContract
// Usage: HARDHAT_NETWORK=mainnet node transfer-ownership.js <contractAddress> <newOwnerAddress>

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

async function transferOwnership(contractAddress, newOwnerAddress) {
    console.log("=== Transfer Ownership of Bridge Contract ===\n");
    
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
    
    if (!newOwnerAddress || !ethers.isAddress(newOwnerAddress)) {
        throw new Error(`Invalid new owner address: ${newOwnerAddress}`);
    }
    
    console.log("Configuration:");
    console.log("- Contract Address:", contractAddress);
    console.log("- New Owner Address:", newOwnerAddress);
    console.log();
    
    // Connect to contract
    console.log("Connecting to contract...");
    const bridge = await ethers.getContractAt("BridgeContract", contractAddress);
    
    // Verify contract exists
    const code = await provider.getCode(contractAddress);
    if (code === "0x") {
        throw new Error(`No contract found at address ${contractAddress}. Please check the address and network.`);
    }
    
    // Get current owner
    let currentOwner;
    try {
        currentOwner = await bridge.owner();
    } catch (error) {
        throw new Error(`Contract at ${contractAddress} is not a BridgeContract or is on a different network. Error: ${error.message}`);
    }
    
    const signerAddress = await signer.getAddress();
    
    console.log("- Current Owner:", currentOwner);
    console.log("- Your Address:", signerAddress);
    console.log();
    
    // Verify signer is current owner
    if (currentOwner.toLowerCase() !== signerAddress.toLowerCase()) {
        throw new Error(`You are not the current owner. Only ${currentOwner} can transfer ownership.`);
    }
    
    // Check if new owner is same as current
    if (currentOwner.toLowerCase() === newOwnerAddress.toLowerCase()) {
        throw new Error(`New owner address is the same as current owner.`);
    }
    
    // Get current gas fees from the network
    const feeData = await provider.getFeeData();
    console.log("Gas fees:");
    console.log("- Max Fee:", ethers.formatUnits(feeData.maxFeePerGas, "gwei"), "gwei");
    console.log("- Priority Fee:", ethers.formatUnits(feeData.maxPriorityFeePerGas, "gwei"), "gwei");
    console.log();
    
    // Transfer ownership
    console.log(`Transferring ownership to ${newOwnerAddress}...`);
    console.log("(Confirm the transaction on your Ledger if using hardware wallet)");
    const tx = await bridge.transferOwnership(newOwnerAddress, {
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
    
    // Verify transfer
    const verifiedOwner = await bridge.owner();
    console.log("Verification:");
    console.log("- New Owner:", verifiedOwner);
    
    if (verifiedOwner.toLowerCase() === newOwnerAddress.toLowerCase()) {
        console.log("\n✓ Ownership transferred successfully!");
    } else {
        throw new Error(`Ownership transfer verification failed. Expected ${newOwnerAddress}, got ${verifiedOwner}`);
    }
    
    return { tx, receipt };
}

// Parse command-line arguments
function parseArgs() {
    const args = process.argv.slice(2);
    
    if (args.length < 2) {
        console.error("Usage: HARDHAT_NETWORK=<network> node transfer-ownership.js <contractAddress> <newOwnerAddress>");
        console.error("\nArguments:");
        console.error("  contractAddress   - Deployed BridgeContract address");
        console.error("  newOwnerAddress   - Address to transfer ownership to");
        console.error("\nExamples:");
        console.error("  HARDHAT_NETWORK=mainnet node transfer-ownership.js 0x1234...abcd 0x5678...efgh");
        process.exit(1);
    }
    
    return {
        contractAddress: args[0],
        newOwnerAddress: args[1]
    };
}

// Main execution
if (import.meta.url === `file://${process.argv[1]}`) {
    const { contractAddress, newOwnerAddress } = parseArgs();
    
    transferOwnership(contractAddress, newOwnerAddress)
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
    transferOwnership
};

