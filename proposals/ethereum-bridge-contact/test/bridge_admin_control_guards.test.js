import { expect } from "chai";
import hre from "hardhat";

describe("BridgeContract admin control guards", function () {
    let ethersInstance;
    let bridge;
    let owner;
    let user;
    let spender;

    const GONKA_CHAIN_ID = "0x" + "01".repeat(32);
    const ETHEREUM_CHAIN_ID = "0x" + "02".repeat(32);
    const EXAMPLE_GROUP_KEY = "0x" + "11".repeat(256);
    const EXAMPLE_VALIDATION_SIG = "0x" + "22".repeat(128);

    beforeEach(async function () {
        const networkConnection = await hre.network.getOrCreate();
        ethersInstance = networkConnection.ethers;
        [owner, user, spender] = await ethersInstance.getSigners();
        const BridgeContract = await ethersInstance.getContractFactory("BridgeContract");
        bridge = await BridgeContract.deploy(GONKA_CHAIN_ID, ETHEREUM_CHAIN_ID);
        await bridge.waitForDeployment();
    });

    it("reverts transfer burn path in ADMIN_CONTROL", async function () {
        await expect(
            bridge.connect(user).transfer(await bridge.getAddress(), 1n)
        ).to.be.revertedWithCustomError(bridge, "BridgeNotOperational");
    });

    it("reverts transferFrom burn path in ADMIN_CONTROL", async function () {
        await expect(
            bridge
                .connect(spender)
                .transferFrom(await user.getAddress(), await bridge.getAddress(), 1n)
        ).to.be.revertedWithCustomError(bridge, "BridgeNotOperational");
    });

    it("reverts public submitGroupKey in ADMIN_CONTROL", async function () {
        await expect(
            bridge.connect(user).submitGroupKey(2, EXAMPLE_GROUP_KEY, EXAMPLE_VALIDATION_SIG)
        ).to.be.revertedWithCustomError(bridge, "BridgeNotOperational");
    });

    it("keeps regular ERC20 transfers unguarded by bridge state", async function () {
        await expect(
            bridge.connect(user).transfer(await spender.getAddress(), 1n)
        ).to.be.revertedWith("ERC20: transfer amount exceeds balance");
    });

    it("allows submitGroupKey path once NORMAL_OPERATION is restored", async function () {
        await bridge.connect(owner).setGroupKey(1, EXAMPLE_GROUP_KEY);
        await bridge.connect(owner).resetToNormalOperation();

        await expect(
            bridge.connect(user).submitGroupKey(3, EXAMPLE_GROUP_KEY, EXAMPLE_VALIDATION_SIG)
        ).to.be.revertedWithCustomError(bridge, "InvalidEpochSequence");
    });
});
