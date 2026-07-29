// End-to-end test for the group-key rotation domain-separation fix, run against
// the real EIP-2537 BLS precompiles (Prague EVM).
import { expect } from "chai";
import hre from "hardhat";
import { bls12_381 as bls } from "@noble/curves/bls12-381.js";
import {
    publicKeyToEip2537Hex,
    signatureToEip2537Hex,
} from "../bls.js";

const GONKA_CHAIN_ID = "0x" + "01".repeat(32);
const ETHEREUM_CHAIN_ID = "0x" + "02".repeat(32);

const SK_N = 0x1234567890abcdef1234567890abcdef1234567890abcdefn;
const SK_N1 = 0x0fedcba0987654321fedcba0987654321fedcba0987654321n;

function groupKey256(sk) {
    const compressed = bls.G2.Point.BASE.multiply(sk).toBytes(true); // 96 bytes
    return publicKeyToEip2537Hex(Buffer.from(compressed).toString("hex"));
}

// Sign a contract message hash the same way the group would: sig = sk * H(m),
// where H(m) = MAP_FP_TO_G1(messageHash) is taken from the real precompile (via
// the harness) so it is byte-identical to what the verifier recomputes.
async function signMessageHash(harness, messageHash, sk) {
    const hmPadded = await harness.exposedMapMessageToG1(messageHash); // 0x + 128 bytes
    const hm = Buffer.from(hmPadded.slice(2), "hex");
    expect(hm.length).to.equal(128);
    // EIP-2537 G1 point: [16-byte pad || 48-byte x][16-byte pad || 48-byte y].
    const raw96 = Buffer.concat([hm.subarray(16, 64), hm.subarray(80, 128)]);
    const hmPoint = bls.G1.Point.fromBytes(raw96);
    const sigRaw = hmPoint.multiply(sk).toBytes(false); // 96-byte uncompressed
    return signatureToEip2537Hex(Buffer.from(sigRaw).toString("hex")); // -> 128 bytes
}

describe("Group-key rotation domain separation (EIP-2537 e2e)", function () {
    let ethers, harness, owner, attacker;
    const N = 5n;

    beforeEach(async function () {
        const conn = await hre.network.getOrCreate();
        ethers = conn.ethers;
        [owner, attacker] = await ethers.getSigners();
        const Harness = await ethers.getContractFactory("BridgeHarness");
        harness = await Harness.deploy(GONKA_CHAIN_ID, ETHEREUM_CHAIN_ID);
        await harness.waitForDeployment();
        await harness.connect(owner).setGroupKey(N, groupKey256(SK_N));
        await harness.connect(owner).resetToNormalOperation();
    });

    it("verifies and installs a legitimately signed rotation", async function () {
        const newKey = groupKey256(SK_N1);
        const msgHash = await harness.transitionMessageHash(newKey, N);
        const sig = await signMessageHash(harness, msgHash, SK_N);

        expect(await harness.exposedVerifyTransition(groupKey256(SK_N), newKey, sig, N))
            .to.equal(true);
        await harness.connect(attacker).submitGroupKey(N + 1n, newKey, sig);
        const meta = await harness.epochMeta();
        expect(meta.latestEpochId).to.equal(N + 1n);
    });

    it("rejects a signature over the OLD untagged message (forge replay is dead)", async function () {
        const newKey = groupKey256(SK_N1);
        const untaggedHash = ethers.solidityPackedKeccak256(
            ["uint64", "bytes32", "bytes"],
            [N, GONKA_CHAIN_ID, newKey]
        );
        const forgedSig = await signMessageHash(harness, untaggedHash, SK_N);
        expect(await harness.exposedVerifyRaw(groupKey256(SK_N), untaggedHash, forgedSig))
            .to.equal(true);
        expect(await harness.exposedVerifyTransition(groupKey256(SK_N), newKey, forgedSig, N))
            .to.equal(false);
        await expect(
            harness.connect(attacker).submitGroupKey(N + 1n, newKey, forgedSig)
        ).to.be.revertedWith("Invalid transition signature");
    });

    it("_isValidG2Point accepts a real DKG key and rejects a non-curve blob", async function () {
        expect(await harness.exposedIsValidG2Point(groupKey256(SK_N1))).to.equal(true);

        const garbage = "0x" + "11".repeat(256); // right length, not a G2 point
        expect(await harness.exposedIsValidG2Point(garbage)).to.equal(false);
    });

    it("submitGroupKey rejects a non-curve garbage key before storing it", async function () {
        const garbage = "0x" + "11".repeat(256);
        const anySig = "0x" + "22".repeat(128);
        await expect(
            harness.connect(attacker).submitGroupKey(N + 1n, garbage, anySig)
        ).to.be.revertedWith("Invalid G2 group key");
    });
});
