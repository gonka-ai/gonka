// Deployment script for BridgeContract
// Usage: HARDHAT_NETWORK=mainnet node deploy.js
// Ledger: Configure LEDGER_ADDRESS in .env, then run normally

import hardhat from "hardhat";
import dotenv from "dotenv";
import { base64ToHex } from "./bls.js";

// Load environment variables from .env file
dotenv.config();

// Helper function to get provider and signer for current network
// Uses Hardhat's built-in signer management (supports Ledger via hardhat-ledger plugin)
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

function evmChainIdToBytes32(ethers, chainId) {
    const value = String(chainId || "").trim();
    if (!value) {
        throw new Error("ETHEREUM_CHAIN_ID is empty");
    }

    if (value.startsWith("0x")) {
        return ethers.zeroPadValue(value, 32);
    }

    return ethers.zeroPadValue(ethers.toBeHex(BigInt(value)), 32);
}

async function main() {
    console.log("Deploying BridgeContract...");
    
    // Get provider and signer (Ledger support via hardhat-ledger plugin)
    const { provider, signer, ethers } = await getProviderAndSigner();
    const deployerAddress = await signer.getAddress();
    console.log("Deploying from:", deployerAddress);
    
    // Define chain IDs for cross-chain replay protection.
    // Must match ctx.ChainID() on the Gonka chain that creates BLS signatures.
    const gonkaChainName = process.env.GONKA_CHAIN_ID || "gonka-mainnet";
    const gonkaChainId = ethers.sha256(ethers.toUtf8Bytes(gonkaChainName));
    
    const networkInfo = await signer.provider.getNetwork();
    const ethereumChainIdInput = process.env.ETHEREUM_CHAIN_ID || networkInfo.chainId;
    const ethereumChainId = evmChainIdToBytes32(ethers, ethereumChainIdInput);
    
    console.log("Chain IDs:");
    console.log("- Gonka Chain Name:", gonkaChainName);
    console.log("- Gonka Chain ID:", gonkaChainId);
    console.log("- Ethereum Chain ID:", ethereumChainId, "(domain", ethereumChainIdInput + ", network", networkInfo.chainId + ")");

    // Deploy the contract using ethers.deployContract (better Ledger support)
    console.log("\nPlease confirm the transaction on your device if using Ledger...");
    const bridge = await ethers.deployContract("BridgeContract", [gonkaChainId, ethereumChainId]);
    
    const deployTx = bridge.deploymentTransaction();
    console.log("Transaction submitted:", deployTx.hash);
    console.log("View on Etherscan: https://etherscan.io/tx/" + deployTx.hash);
    console.log("Waiting for confirmation...");
    await bridge.waitForDeployment();

    const contractAddress = await bridge.getAddress();
    console.log("BridgeContract deployed to:", contractAddress);

    // Verify the initial state
    const currentState = await bridge.getCurrentState();
    const latestEpoch = await bridge.getLatestEpochInfo();
    
    console.log("\nInitial State:");
    console.log("- Contract State:", currentState === 0n ? "ADMIN_CONTROL" : "NORMAL_OPERATION");
    console.log("- Latest Epoch ID:", latestEpoch.epochId.toString());
    console.log("- Contract Owner:", await bridge.owner());

    console.log("\nNext Steps:");
    console.log("1. Submit genesis epoch (epoch 1) group key:");
    console.log("   bridge.setGroupKey(1, genesisGroupKey)");
    console.log("2. Reset to normal operation:");
    console.log("   bridge.resetToNormalOperation()");

    // Return contract instance for further operations
    return bridge;
}

// Example usage functions for testing
async function submitGenesisEpoch(bridgeAddress, groupPublicKey) {
    const { ethers } = await getProviderAndSigner();
    const bridge = await ethers.getContractAt("BridgeContract", bridgeAddress);
    const contractGroupPublicKey = base64ToHex(groupPublicKey);

    console.log("Submitting genesis epoch (epoch 1)...");
    
    const tx = await bridge.setGroupKey(
        1, // epochId
        contractGroupPublicKey // 256-byte EIP-2537 G2 public key
    );
    
    await tx.wait();
    console.log("Genesis epoch submitted! Transaction:", tx.hash);

    return tx;
}

async function enableNormalOperation(bridgeAddress) {
    const { ethers } = await getProviderAndSigner();
    const bridge = await ethers.getContractAt("BridgeContract", bridgeAddress);

    console.log("Enabling normal operation...");
    
    const tx = await bridge.resetToNormalOperation();
    await tx.wait();
    
    console.log("Normal operation enabled! Transaction:", tx.hash);
    
    const newState = await bridge.getCurrentState();
    console.log("Current state:", newState === 0n ? "ADMIN_CONTROL" : "NORMAL_OPERATION");

    return tx;
}

// Example withdrawal function for testing
async function testWithdrawal(bridgeAddress, withdrawalCommand) {
    const { ethers } = await getProviderAndSigner();
    const bridge = await ethers.getContractAt("BridgeContract", bridgeAddress);

    console.log("Testing withdrawal...");
    console.log("Command:", withdrawalCommand);

    try {
        const tx = await bridge.withdraw(withdrawalCommand);
        await tx.wait();
        console.log("Withdrawal successful! Transaction:", tx.hash);
        return tx;
    } catch (error) {
        console.error("Withdrawal failed:", error.message);
        throw error;
    }
}

// Helper function to create example withdrawal command
function createWithdrawalCommand(epochId, requestId, recipient, tokenContract, amount) {
    return {
        epochId: epochId,
        requestId: requestId,
        recipient: recipient,
        tokenContract: tokenContract,
        amount: amount,
        signature: "0x" + "00".repeat(128) // Placeholder - replace with actual EIP-2537 BLS signature
    };
}

// Example BLS group public key (placeholder - replace with actual key)
const EXAMPLE_GROUP_PUBLIC_KEY = "0x" + "00".repeat(256);

// Export functions for use in other scripts
export {
    main,
    getProviderAndSigner,
    submitGenesisEpoch,
    enableNormalOperation,
    testWithdrawal,
    createWithdrawalCommand,
    EXAMPLE_GROUP_PUBLIC_KEY
};

// Run deployment if script is executed directly
// Note: When using 'npx hardhat run', the script is always executed
main()
    .then(() => process.exit(0))
    .catch((error) => {
        console.error(error);
        process.exit(1);
    });
