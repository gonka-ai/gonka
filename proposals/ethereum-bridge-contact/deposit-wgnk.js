#!/usr/bin/env node
// CLI tool to deposit WGNK tokens to the bridge (auto-burn mechanism)
// Usage: node deposit-wgnk.js <contractAddress> <amount>

import hre from "hardhat";
import { ethers } from "ethers";
import dotenv from "dotenv";

// Load environment variables
dotenv.config();

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

async function depositWGNK(contractAddress, amount) {
    console.log("=== Deposit WGNK to Bridge ===\n");
    
    const { provider, signer, ethers } = await getProviderAndSigner();
    
    // Show network info
    const network = await provider.getNetwork();
    console.log("Network:", network.name, `(chainId: ${network.chainId})`);
    console.log();
    
    // Validate inputs and normalize addresses to checksummed format
    if (!contractAddress || !ethers.isAddress(contractAddress)) {
        throw new Error(`Invalid contract address: ${contractAddress}`);
    }
    contractAddress = ethers.getAddress(contractAddress); // Normalize to checksummed format
    
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
    
    const signerAddress = await signer.getAddress();
    
    console.log("Configuration:");
    console.log("- Contract Address:", contractAddress);
    console.log("- Your Address:", signerAddress);
    console.log("- Amount:", amountBigInt.toString(), "wei");
    console.log();
    
    // Connect to contract
    console.log("Connecting to BridgeContract/WGNK token...");
    const artifact = await hre.artifacts.readArtifact("BridgeContract");
    const bridge = new ethers.Contract(contractAddress, artifact.abi, signer);
    
    // Verify contract exists and is a BridgeContract
    const code = await provider.getCode(contractAddress);
    if (code === "0x") {
        throw new Error(`No contract found at address ${contractAddress}. Please check the address and network.`);
    }
    
    // Get WGNK token info
    let tokenInfo;
    try {
        tokenInfo = await bridge.getWGNKInfo();
        console.log(`- Token: ${tokenInfo.tokenName} (${tokenInfo.tokenSymbol})`);
        console.log(`- Decimals: ${tokenInfo.tokenDecimals}`);
        console.log(`- Total Supply: ${tokenInfo.tokenTotalSupply.toString()} wei`);
    } catch (error) {
        throw new Error(`Contract at ${contractAddress} is not a BridgeContract or is on a different network. Error: ${error.message}`);
    }
    console.log();
    
    // Check current balance
    const balanceBefore = await bridge.balanceOf(signerAddress);
    console.log("Current Balance:", balanceBefore.toString(), "wei");
    
    if (balanceBefore < amountBigInt) {
        throw new Error(`Insufficient balance. You have ${balanceBefore.toString()} wei, but trying to deposit ${amountBigInt.toString()} wei.`);
    }
    console.log();
    
    // Estimate gas first to catch errors
    console.log(`Estimating gas for deposit (transfer to bridge)...`);
    try {
        const gasEstimate = await bridge.transfer.estimateGas(contractAddress, amountBigInt);
        console.log("- Estimated Gas:", gasEstimate.toString());
    } catch (estimateError) {
        console.error("\n❌ Transaction simulation failed:");
        console.error("Error:", estimateError.message);
        throw estimateError;
    }
    console.log();
    
    // Execute deposit (transfer to contract address triggers auto-burn)
    console.log(`Processing deposit...`);
    console.log(`Note: Transferring WGNK to the bridge contract triggers auto-burn.`);
    const tx = await bridge.transfer(contractAddress, amountBigInt);
    
    console.log("✓ Transaction sent:", tx.hash);
    console.log("Waiting for confirmation...");
    
    const receipt = await tx.wait();
    console.log("✓ Transaction confirmed!");
    console.log("- Block Number:", receipt.blockNumber);
    console.log("- Gas Used:", receipt.gasUsed.toString());
    console.log();
    
    // Check for WGNKBurned event
    const burnEvent = receipt.logs.find(log => {
        try {
            const parsed = bridge.interface.parseLog(log);
            return parsed && parsed.name === 'WGNKBurned';
        } catch {
            return false;
        }
    });
    
    if (burnEvent) {
        const parsed = bridge.interface.parseLog(burnEvent);
        console.log("Burn Event Details:");
        console.log("- From:", parsed.args.from);
        console.log("- Amount:", parsed.args.amount.toString(), "wei");
        console.log("- Timestamp:", parsed.args.timestamp.toString());
        console.log();
    }
    
    // Get updated balance
    const balanceAfter = await bridge.balanceOf(signerAddress);
    console.log("Balance After Deposit:");
    console.log("- Before:", balanceBefore.toString(), "wei");
    console.log("- After:", balanceAfter.toString(), "wei");
    console.log("- Burned:", (balanceBefore - balanceAfter).toString(), "wei");
    
    // Get updated total supply
    const totalSupplyAfter = await bridge.totalSupply();
    console.log("\nTotal Supply After Burn:", totalSupplyAfter.toString(), "wei");
    
    console.log("\n✓ WGNK deposit (burn) processed successfully!");
    console.log("\nWhat happened:");
    console.log("- Your WGNK tokens were transferred to the bridge contract address");
    console.log("- The bridge's transfer() function detected the recipient is itself");
    console.log("- This triggered the auto-burn mechanism (see BridgeContract.sol lines 437-441)");
    console.log("- Your tokens are now burned and removed from circulation");
    console.log("- This should trigger corresponding actions on the Gonka chain");
    
    return { tx, receipt };
}

// Parse command-line arguments
function parseArgs() {
    const args = process.argv.slice(2);
    
    if (args.length < 2) {
        console.error("Usage: node deposit-wgnk.js <contractAddress> <amount>");
        console.error("\nArguments:");
        console.error("  contractAddress  - Deployed BridgeContract address (which is also the WGNK token)");
        console.error("  amount           - Amount of WGNK to deposit/burn (in wei)");
        console.error("\nHow it works:");
        console.error("  - The script transfers WGNK to the bridge contract address itself");
        console.error("  - BridgeContract.transfer() detects recipient == address(this)");
        console.error("  - This triggers auto-burn: tokens are removed from circulation");
        console.error("  - WGNKBurned event is emitted with (from, amount, timestamp)");
        console.error("\nRequirements:");
        console.error("  - You must have sufficient WGNK balance");
        console.error("  - Set PRIVATE_KEY in .env file");
        console.error("  - Set network RPC URL in .env (e.g., SEPOLIA_RPC_URL)");
        console.error("\nExamples:");
        console.error("  # Deposit 100 WGNK (with 18 decimals)");
        console.error('  node deposit-wgnk.js 0x1234... 100000000000000000000');
        console.error("\n  # Deposit 0.5 WGNK");
        console.error('  node deposit-wgnk.js 0x1234... 500000000000000000');
        console.error("\nNote:");
        console.error("  - amount is in wei (1 WGNK = 10^18 wei if using 18 decimals)");
        console.error("  - This operation is irreversible - tokens are burned");
        console.error("  - Make sure you have the correct bridge contract address");
        process.exit(1);
    }
    
    return {
        contractAddress: args[0],
        amount: args[1]
    };
}

// Main execution
if (import.meta.url === `file://${process.argv[1]}`) {
    const { contractAddress, amount } = parseArgs();
    
    depositWGNK(contractAddress, amount)
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
    depositWGNK
};

