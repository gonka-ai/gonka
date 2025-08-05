// Deployment script for BridgeContract
// Usage: npx hardhat run deploy.js --network <network>

const { ethers } = require("hardhat");

async function main() {
    console.log("Deploying BridgeContract...");

    // Get the contract factory
    const BridgeContract = await ethers.getContractFactory("BridgeContract");

    // Deploy the contract
    const bridge = await BridgeContract.deploy();
    await bridge.deployed();

    console.log("BridgeContract deployed to:", bridge.address);
    console.log("Transaction hash:", bridge.deployTransaction.hash);

    // Verify the initial state
    const currentState = await bridge.getCurrentState();
    const latestEpoch = await bridge.getLatestEpochInfo();
    
    console.log("\nInitial State:");
    console.log("- Contract State:", currentState === 0 ? "ADMIN_CONTROL" : "NORMAL_OPERATION");
    console.log("- Latest Epoch ID:", latestEpoch.epochId.toString());
    console.log("- Contract Owner:", await bridge.owner());

    console.log("\nNext Steps:");
    console.log("1. Submit genesis epoch (epoch 1) group key:");
    console.log("   bridge.submitGroupKey(1, genesisGroupKey, '0x')");
    console.log("2. Reset to normal operation:");
    console.log("   bridge.resetToNormalOperation()");

    // Return contract instance for further operations
    return bridge;
}

// Example usage functions for testing
async function submitGenesisEpoch(bridgeAddress, groupPublicKey) {
    const BridgeContract = await ethers.getContractFactory("BridgeContract");
    const bridge = BridgeContract.attach(bridgeAddress);

    console.log("Submitting genesis epoch (epoch 1)...");
    
    const tx = await bridge.submitGroupKey(
        1, // epochId
        groupPublicKey, // 96-byte G2 public key
        "0x" // empty validation signature for genesis
    );
    
    await tx.wait();
    console.log("Genesis epoch submitted! Transaction:", tx.hash);

    return tx;
}

async function enableNormalOperation(bridgeAddress) {
    const BridgeContract = await ethers.getContractFactory("BridgeContract");
    const bridge = BridgeContract.attach(bridgeAddress);

    console.log("Enabling normal operation...");
    
    const tx = await bridge.resetToNormalOperation();
    await tx.wait();
    
    console.log("Normal operation enabled! Transaction:", tx.hash);
    
    const newState = await bridge.getCurrentState();
    console.log("Current state:", newState === 0 ? "ADMIN_CONTROL" : "NORMAL_OPERATION");

    return tx;
}

// Example withdrawal function for testing
async function testWithdrawal(bridgeAddress, withdrawalCommand) {
    const BridgeContract = await ethers.getContractFactory("BridgeContract");
    const bridge = BridgeContract.attach(bridgeAddress);

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
        signature: "0x" + "00".repeat(48) // Placeholder - replace with actual BLS signature
    };
}

// Example BLS group public key (placeholder - replace with actual key)
const EXAMPLE_GROUP_PUBLIC_KEY = "0x" + "00".repeat(96);

// Export functions for use in other scripts
module.exports = {
    main,
    submitGenesisEpoch,
    enableNormalOperation,
    testWithdrawal,
    createWithdrawalCommand,
    EXAMPLE_GROUP_PUBLIC_KEY
};

// Run deployment if script is executed directly
if (require.main === module) {
    main()
        .then(() => process.exit(0))
        .catch((error) => {
            console.error(error);
            process.exit(1);
        });
}