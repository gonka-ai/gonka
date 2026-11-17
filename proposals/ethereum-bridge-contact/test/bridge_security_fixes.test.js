import { expect } from "chai";
import hre from "hardhat";
import { bls12_381 as bls } from "@noble/curves/bls12-381.js";

const GONKA_CHAIN_ID = "0x" + "01".repeat(32);
const ETHEREUM_CHAIN_ID = "0x" + "02".repeat(32);
const REQUEST_ID = "0x" + "ab".repeat(32);
const FP_MODULUS = bls.fields.Fp.ORDER;

const fp = (value) => value.toString(16).padStart(128, "0");
const g1 = (point) => {
    const { x, y } = point.toAffine();
    return "0x" + fp(x) + fp(y);
};
const secret = (epoch) => 100n + epoch;
const groupKey = (epoch) => {
    const { x, y } = bls.G2.Point.BASE.multiply(secret(epoch)).toAffine();
    // EIP-2537 uses c0,c1 order, with each field element padded to 64 bytes.
    return "0x" + fp(x.c0) + fp(x.c1) + fp(y.c0) + fp(y.c1);
};

// Gonka signs MAP_FP_TO_G1(keccak256(packed payload)), not the standard BLS
// hash-to-curve ciphersuite. Compute this independently with noble; all tests
// exercise the real MAP_FP_TO_G1 and pairing precompiles in BridgeContract.
const signDigest = (digest, epoch) => g1(bls.G1.mapToCurve(BigInt(digest)).multiply(secret(epoch)));

describe("BridgeContract security fixes (#1080)", function () {
    let ethers, bridge, owner, relayer, recipient, otherRecipient;

    async function deployBridge() {
        const contract = await (await ethers.getContractFactory("BridgeContract")).deploy(GONKA_CHAIN_ID, ETHEREUM_CHAIN_ID);
        await contract.waitForDeployment();
        await contract.setGroupKey(1n, groupKey(1n));
        await contract.setGroupKey(2n, groupKey(2n));
        await contract.resetToNormalOperation();
        return contract;
    }

    async function advanceEpoch(epoch) {
        const key = groupKey(epoch);
        const digest = ethers.solidityPackedKeccak256(
            ["uint64", "bytes32", "bytes"], [epoch - 1n, GONKA_CHAIN_ID, key],
        );
        await bridge.connect(relayer).submitGroupKey(epoch, key, signDigest(digest, epoch - 1n));
    }

    async function signedCommand(operation, overrides = {}, target = bridge, domains = {}) {
        const cmd = {
            epochId: 1n, requestId: REQUEST_ID, recipient: recipient.address, amount: 100n,
            ...(operation === "withdraw" ? { tokenContract: await target.getAddress() } : {}),
            ...overrides,
        };
        const fields = ["uint64", "bytes32", "bytes32", "bytes32", "bytes32", "address", "address"];
        const values = [
            cmd.epochId, domains.gonka ?? GONKA_CHAIN_ID, cmd.requestId,
            domains.ethereum ?? ETHEREUM_CHAIN_ID,
            ethers.id(operation === "withdraw" ? "WITHDRAW_OPERATION" : "MINT_OPERATION"),
            cmd.recipient, await target.getAddress(),
        ];
        if (operation === "withdraw") {
            fields.push("address");
            values.push(cmd.tokenContract);
        }
        fields.push("uint256");
        values.push(cmd.amount);
        cmd.signature = signDigest(ethers.solidityPackedKeccak256(fields, values), cmd.epochId);
        return cmd;
    }

    const redeem = (operation, cmd, target = bridge) => operation === "withdraw"
        ? target.connect(relayer).withdraw(cmd)
        : target.connect(relayer).mintWithSignature(cmd);

    beforeEach(async function () {
        ({ ethers } = await hre.network.getOrCreate());
        [owner, relayer, recipient, otherRecipient] = await ethers.getSigners();
        bridge = await deployBridge();
        await owner.sendTransaction({ to: await bridge.getAddress(), value: 1_000n });
    });

    for (const operation of ["mint", "withdraw"]) {
        describe(operation, function () {
            it("executes a real BLS signature once and rejects same- and cross-epoch replay", async function () {
                const first = await signedCommand(operation);
                const later = await signedCommand(operation, { epochId: 2n });
                const balanceBefore = operation === "mint"
                    ? await bridge.balanceOf(recipient.address)
                    : await ethers.provider.getBalance(recipient.address);
                await expect(redeem(operation, first)).to.emit(
                    bridge, operation === "mint" ? "WGNKMinted" : "WithdrawalProcessed",
                );
                const balanceAfter = operation === "mint"
                    ? await bridge.balanceOf(recipient.address)
                    : await ethers.provider.getBalance(recipient.address);
                expect(balanceAfter - balanceBefore).to.equal(first.amount);
                for (const epoch of [1n, 2n, 99n]) {
                    expect(await bridge.isRequestProcessed(epoch, REQUEST_ID)).to.equal(true);
                    expect(await bridge.processedRequests(epoch, REQUEST_ID)).to.equal(true);
                }
                await expect(redeem(operation, first)).to.be.revertedWithCustomError(bridge, "RequestAlreadyProcessed");
                await expect(redeem(operation, later)).to.be.revertedWithCustomError(bridge, "RequestAlreadyProcessed");
            });

            it("accepts a replacement signature after the old epoch expires, without double redemption", async function () {
                const original = await signedCommand(operation);
                await advanceEpoch(3n);
                await advanceEpoch(4n);
                await expect(redeem(operation, original)).to.be.revertedWithCustomError(bridge, "EpochTooOld");
                expect(await bridge.isRequestProcessed(1n, REQUEST_ID)).to.equal(false);
                const replacement = await signedCommand(operation, { epochId: 4n });
                await redeem(operation, replacement);
                expect(await bridge.isRequestProcessed(1n, REQUEST_ID)).to.equal(true);
                await expect(redeem(operation, replacement)).to.be.revertedWithCustomError(bridge, "RequestAlreadyProcessed");
            });

            it("accepts the inclusive two-epoch boundary", async function () {
                await advanceEpoch(3n);
                await advanceEpoch(4n);
                await redeem(operation, await signedCommand(operation, { epochId: 2n }));
            });

            it("does not consume a request id when the signature is invalid", async function () {
                const cmd = await signedCommand(operation);
                const wrong = signDigest(ethers.id("different payload"), cmd.epochId);
                await expect(redeem(operation, { ...cmd, signature: wrong })).to.be.revertedWithCustomError(bridge, "InvalidSignature");
                expect(await bridge.isRequestProcessed(1n, REQUEST_ID)).to.equal(false);
                await redeem(operation, cmd);
                expect(await bridge.isRequestProcessed(1n, REQUEST_ID)).to.equal(true);
            });

            for (const field of ["epochId", "requestId", "recipient", "amount", ...(operation === "withdraw" ? ["tokenContract"] : [])]) {
                it(`rejects a signature with a changed ${field}`, async function () {
                    const cmd = await signedCommand(operation);
                    const changes = {
                        epochId: 2n, requestId: ethers.id("another request"), recipient: otherRecipient.address,
                        amount: cmd.amount + 1n, tokenContract: otherRecipient.address,
                    };
                    await expect(redeem(operation, { ...cmd, [field]: changes[field] })).to.be.revertedWithCustomError(bridge, "InvalidSignature");
                });
            }

            for (const domain of ["gonka", "ethereum"]) {
                it(`rejects a signature from another ${domain} chain`, async function () {
                    const cmd = await signedCommand(operation, {}, bridge, { [domain]: ethers.id("other chain") });
                    await expect(redeem(operation, cmd)).to.be.revertedWithCustomError(bridge, "InvalidSignature");
                });
            }

            it("rejects a signature addressed to a different bridge contract", async function () {
                const anotherBridge = await deployBridge();
                const cmd = await signedCommand(operation);
                await expect(redeem(operation, cmd, anotherBridge)).to.be.revertedWithCustomError(anotherBridge, "InvalidSignature");
                expect(await anotherBridge.isRequestProcessed(1n, REQUEST_ID)).to.equal(false);
            });

            it("rejects zero amounts before processing a request", async function () {
                await expect(redeem(operation, await signedCommand(operation, { amount: 0n })))
                    .to.be.revertedWithCustomError(bridge, "InvalidAmount");
                expect(await bridge.isRequestProcessed(1n, REQUEST_ID)).to.equal(false);
            });
        });
    }

    it("rejects a mint signature used for a withdrawal and vice versa", async function () {
        const mint = await signedCommand("mint");
        const withdrawal = await signedCommand("withdraw");
        await expect(bridge.withdraw({ ...withdrawal, signature: mint.signature })).to.be.revertedWithCustomError(bridge, "InvalidSignature");
        await expect(bridge.mintWithSignature({ ...mint, signature: withdrawal.signature })).to.be.revertedWithCustomError(bridge, "InvalidSignature");
    });

    it("consumes a request id across operations as well as epochs", async function () {
        await redeem("mint", await signedCommand("mint"));
        await expect(redeem("withdraw", await signedCommand("withdraw", { epochId: 2n })))
            .to.be.revertedWithCustomError(bridge, "RequestAlreadyProcessed");
    });

    it("rolls back the processed flag when an ETH transfer fails, allowing a later retry", async function () {
        const cmd = await signedCommand("withdraw", { amount: 2_000n });
        await expect(bridge.withdraw(cmd)).to.be.revertedWith("Insufficient ETH balance");
        expect(await bridge.isRequestProcessed(1n, REQUEST_ID)).to.equal(false);
        await owner.sendTransaction({ to: await bridge.getAddress(), value: 1_000n });
        await expect(bridge.withdraw(cmd)).to.emit(bridge, "WithdrawalProcessed");
        expect(await bridge.isRequestProcessed(2n, REQUEST_ID)).to.equal(true);
    });

    it("withdraws ERC-20 tokens with a real signature and blocks reissue replay", async function () {
        const token = await (await ethers.getContractFactory("BridgeTestToken")).deploy(await bridge.getAddress(), 1_000n);
        await token.waitForDeployment();
        const cmd = await signedCommand("withdraw", { tokenContract: await token.getAddress() });
        await bridge.withdraw(cmd);
        expect(await token.balanceOf(recipient.address)).to.equal(cmd.amount);
        const reissued = await signedCommand("withdraw", { tokenContract: await token.getAddress(), epochId: 2n });
        await expect(bridge.withdraw(reissued)).to.be.revertedWithCustomError(bridge, "RequestAlreadyProcessed");
    });

    it("rolls back the processed flag when an external token transfer reverts", async function () {
        const token = await (await ethers.getContractFactory("BridgeTestToken")).deploy(owner.address, 2_000n);
        await token.waitForDeployment();
        await token.transfer(await bridge.getAddress(), 1_000n);
        const cmd = await signedCommand("withdraw", { tokenContract: await token.getAddress(), amount: 2_000n });
        await expect(bridge.withdraw(cmd)).to.be.revertedWith("ERC20: transfer amount exceeds balance");
        expect(await bridge.isRequestProcessed(1n, REQUEST_ID)).to.equal(false);
        expect(await token.balanceOf(recipient.address)).to.equal(0n);
        await token.transfer(await bridge.getAddress(), 1_000n);
        await bridge.withdraw(cmd);
        expect(await token.balanceOf(recipient.address)).to.equal(cmd.amount);
        expect(await bridge.isRequestProcessed(2n, REQUEST_ID)).to.equal(true);
    });

    describe("G1 coordinate negation", function () {
        let harness;
        beforeEach(async function () {
            harness = await (await ethers.getContractFactory("BridgeContractHarness")).deploy();
            await harness.waitForDeployment();
        });
        for (const y of [0n, 1n, FP_MODULUS - 1n]) {
            it(`negates the canonical boundary y=${y === 0n ? "0" : y === 1n ? "1" : "p-1"}`, async function () {
                const point = "0x" + fp(7n) + fp(y);
                expect(await harness.negateG1(point)).to.equal("0x" + fp(7n) + fp(y === 0n ? 0n : FP_MODULUS - y));
            });
        }
        it("preserves the all-zero point at infinity", async function () {
            const infinity = "0x" + "00".repeat(128);
            expect(await harness.negateG1(infinity)).to.equal(infinity);
        });
        for (const y of [FP_MODULUS, FP_MODULUS + 1n, (1n << 512n) - 1n]) {
            it(`rejects the non-canonical coordinate ${y === FP_MODULUS ? "p" : y === FP_MODULUS + 1n ? "p+1" : "2^512-1"}`, async function () {
                await expect(harness.negateG1("0x" + fp(7n) + fp(y))).to.be.revertedWith("G1 y coordinate out of field range");
            });
        }
        it("negates a real curve point exactly as the independent BLS library", async function () {
            const point = bls.G1.Point.BASE.multiply(17n);
            expect(await harness.negateG1(g1(point))).to.equal(g1(point.negate()));
        });
        it("rejects invalid point lengths", async function () {
            await expect(harness.negateG1("0x" + "00".repeat(127))).to.be.revertedWith("Invalid G1 length");
        });
    });
});
