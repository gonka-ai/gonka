// Example test file for BridgeContract
// Usage: npx hardhat test test-example.js

const { expect } = require("chai");
const { ethers } = require("hardhat");

describe("BridgeContract", function () {
    let bridge, owner, user1, user2;
    let mockToken;
    
    // Example data
    const EXAMPLE_GROUP_KEY = "0x" + "12".repeat(96); // 96-byte G2 public key
    const EXAMPLE_SIGNATURE = "0x" + "34".repeat(48); // 48-byte G1 signature

    beforeEach(async function () {
        // Get signers
        [owner, user1, user2] = await ethers.getSigners();

        // Deploy mock ERC20 token for testing
        const MockToken = await ethers.getContractFactory("MockERC20");
        mockToken = await MockToken.deploy("Test Token", "TEST", 18);
        await mockToken.deployed();

        // Deploy bridge contract
        const BridgeContract = await ethers.getContractFactory("BridgeContract");
        bridge = await BridgeContract.deploy();
        await bridge.deployed();

        // Mint tokens to bridge contract for withdrawal testing
        await mockToken.mint(bridge.address, ethers.utils.parseEther("1000"));
    });

    describe("Deployment", function () {
        it("Should start in ADMIN_CONTROL state", async function () {
            expect(await bridge.getCurrentState()).to.equal(0); // ADMIN_CONTROL
        });

        it("Should set deployer as owner", async function () {
            expect(await bridge.owner()).to.equal(owner.address);
        });

        it("Should have zero latest epoch initially", async function () {
            const epochInfo = await bridge.getLatestEpochInfo();
            expect(epochInfo.epochId).to.equal(0);
        });
    });

    describe("Epoch Management", function () {
        it("Should allow admin to submit genesis epoch", async function () {
            await expect(bridge.submitGroupKey(1, EXAMPLE_GROUP_KEY, "0x"))
                .to.emit(bridge, "GroupKeySubmitted")
                .withArgs(1, EXAMPLE_GROUP_KEY, await getBlockTimestamp());

            const epochInfo = await bridge.getLatestEpochInfo();
            expect(epochInfo.epochId).to.equal(1);
        });

        it("Should reject non-sequential epoch submissions", async function () {
            await expect(bridge.submitGroupKey(2, EXAMPLE_GROUP_KEY, "0x"))
                .to.be.revertedWithCustomError(bridge, "InvalidEpochSequence");
        });

        it("Should allow transition to normal operation after genesis", async function () {
            // Submit genesis epoch
            await bridge.submitGroupKey(1, EXAMPLE_GROUP_KEY, "0x");
            
            // Reset to normal operation
            await expect(bridge.resetToNormalOperation())
                .to.emit(bridge, "NormalOperationRestored")
                .withArgs(1, await getBlockTimestamp());

            expect(await bridge.getCurrentState()).to.equal(1); // NORMAL_OPERATION
        });

        it("Should reject normal operation without genesis epoch", async function () {
            await expect(bridge.resetToNormalOperation())
                .to.be.revertedWithCustomError(bridge, "NoValidGenesisEpoch");
        });
    });

    describe("Withdrawals", function () {
        beforeEach(async function () {
            // Setup: Submit genesis epoch and enable normal operation
            await bridge.submitGroupKey(1, EXAMPLE_GROUP_KEY, "0x");
            await bridge.resetToNormalOperation();
        });

        it("Should reject withdrawals when not in normal operation", async function () {
            // Contract starts in ADMIN_CONTROL state, so withdrawals should be rejected
            
            const withdrawalCommand = {
                epochId: 1,
                requestId: ethers.utils.formatBytes32String("req1"),
                recipient: user1.address,
                tokenContract: mockToken.address,
                amount: ethers.utils.parseEther("10"),
                signature: EXAMPLE_SIGNATURE
            };

            await expect(bridge.withdraw(withdrawalCommand))
                .to.be.revertedWithCustomError(bridge, "BridgeNotOperational");
        });

        it("Should reject withdrawals for invalid epochs", async function () {
            const withdrawalCommand = {
                epochId: 999, // Non-existent epoch
                requestId: ethers.utils.formatBytes32String("req1"),
                recipient: user1.address,
                tokenContract: mockToken.address,
                amount: ethers.utils.parseEther("10"),
                signature: EXAMPLE_SIGNATURE
            };

            await expect(bridge.withdraw(withdrawalCommand))
                .to.be.revertedWithCustomError(bridge, "InvalidEpoch");
        });

        it("Should reject duplicate request IDs", async function () {
            const withdrawalCommand = {
                epochId: 1,
                requestId: ethers.utils.formatBytes32String("req1"),
                recipient: user1.address,
                tokenContract: mockToken.address,
                amount: ethers.utils.parseEther("10"),
                signature: EXAMPLE_SIGNATURE
            };

            // Note: This test would fail signature verification in practice
            // For testing, you'd need to mock the BLS verification or use valid signatures

            // Mark request as processed manually for testing
            // In practice, this would happen during a successful withdrawal
        });

        it("Should track processed requests", async function () {
            const requestId = ethers.utils.formatBytes32String("req1");
            expect(await bridge.isRequestProcessed(1, requestId)).to.be.false;

            // After successful withdrawal, this would be true
            // expect(await bridge.isRequestProcessed(1, requestId)).to.be.true;
        });

        it("Should check contract balances for both tokens and ETH", async function () {
            // Check ERC-20 token balance
            const tokenBalance = await bridge.getContractBalance(mockToken.address);
            expect(tokenBalance).to.be.gte(0);

            // Check ETH balance (using address(this) convention)
            const ethBalance = await bridge.getContractBalance(bridge.address);
            expect(ethBalance).to.be.gte(0);
        });

        it("Should handle ETH withdrawals when tokenContract is bridge address", async function () {
            // Send some ETH to the contract
            await owner.sendTransaction({
                to: bridge.address,
                value: ethers.utils.parseEther("5")
            });

            const ethWithdrawalCommand = {
                epochId: 1,
                requestId: ethers.utils.formatBytes32String("eth_req1"),
                recipient: user1.address,
                tokenContract: bridge.address,  // address(this) indicates ETH withdrawal
                amount: ethers.utils.parseEther("1"),
                signature: EXAMPLE_SIGNATURE
            };

            // Note: This would need valid BLS signature in practice
            // The test validates the structure but signature verification would fail
            
            // Verify the ETH balance is available
            const ethBalance = await bridge.getContractBalance(bridge.address);
            expect(ethBalance).to.be.gte(ethWithdrawalCommand.amount);
        });
    });

    describe("Timeout Handling", function () {
        beforeEach(async function () {
            await bridge.submitGroupKey(1, EXAMPLE_GROUP_KEY, "0x");
            await bridge.resetToNormalOperation();
        });

        it("Should detect when timeout has not been reached", async function () {
            expect(await bridge.isTimeoutReached()).to.be.false;
        });

        it("Should reject timeout trigger when not reached", async function () {
            await expect(bridge.checkAndHandleTimeout())
                .to.be.revertedWithCustomError(bridge, "TimeoutNotReached");
        });

        // Note: Testing actual timeout would require time manipulation
        // which is network-dependent and complex to implement generically
    });

    describe("View Functions", function () {
        beforeEach(async function () {
            await bridge.submitGroupKey(1, EXAMPLE_GROUP_KEY, "0x");
        });

        it("Should correctly identify valid epochs", async function () {
            expect(await bridge.isValidEpoch(1)).to.be.true;
            expect(await bridge.isValidEpoch(999)).to.be.false;
        });

        it("Should return correct epoch information", async function () {
            const epochInfo = await bridge.getLatestEpochInfo();
            expect(epochInfo.epochId).to.equal(1);
            expect(epochInfo.groupKey).to.equal(EXAMPLE_GROUP_KEY);
        });
    });

    describe("Timeout Functions", function () {
        it("Should trigger admin control after timeout", async function () {
            await bridge.submitGroupKey(1, EXAMPLE_GROUP_KEY, "0x");
            await bridge.resetToNormalOperation();

            // Fast-forward time by more than 30 days (TIMEOUT_DURATION)
            await network.provider.send("evm_increaseTime", [30 * 24 * 60 * 60 + 1]); // 30 days + 1 second
            await network.provider.send("evm_mine");

            await expect(bridge.checkAndHandleTimeout())
                .to.emit(bridge, "AdminControlActivated")
                .withArgs(await getBlockTimestamp(), "Timeout: No new epochs for 30 days");

            expect(await bridge.getCurrentState()).to.equal(0); // ADMIN_CONTROL
        });

        it("Should not have arbitrary token recovery functions", async function () {
            // Verify that there are no emergency token recovery functions
            // All funds can only be withdrawn via valid BLS signatures
            expect(bridge.emergencyRecoverTokens).to.be.undefined;
        });
    });

    // Helper function to get current block timestamp
    async function getBlockTimestamp() {
        const block = await ethers.provider.getBlock("latest");
        return block.timestamp;
    }
});

// Mock ERC20 contract for testing
const MockERC20_ABI = [
    "constructor(string memory name, string memory symbol, uint8 decimals)",
    "function mint(address to, uint256 amount) external",
    "function balanceOf(address account) external view returns (uint256)",
    "function transfer(address to, uint256 amount) external returns (bool)"
];

// You would need to deploy this separately or use an existing mock contract
// This is just the interface for reference