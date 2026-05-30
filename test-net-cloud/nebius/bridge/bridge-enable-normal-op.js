const { ethers } = require("ethers");
const fs = require("fs");
const http = require("http");
const https = require("https");
const path = require("path");
const { pathToFileURL } = require("url");

const args = process.argv.slice(2);
function getArg(flag) {
    const idx = args.indexOf(flag);
    return idx !== -1 ? args[idx + 1] : null;
}

const bridgeAddress = resolveBridgeAddress();
const defaultEthRpcUrl = process.env.SEPOLIA_RPC_URL || "https://ethereum-sepolia-rpc.publicnode.com";
const ethRpcUrl = getArg("--rpc") || defaultEthRpcUrl;
const ethPrivateKey = getArg("--eth-key") || process.env.PRIVATE_KEY;
const gonkaApi = (getArg("--api") || "http://89.169.111.79:8000").replace(/\/$/, "");
const epochArg = getArg("--epoch");
const groupKeyArg = getArg("--group-key");
const targetEpochArg = getArg("--target-epoch");

if (!ethPrivateKey || !bridgeAddress) {
    console.log(`
Usage: node bridge-enable-normal-op.js \\
  --eth-key <ETH_PRIVATE_KEY> \\
  --bridge <BRIDGE_CONTRACT_ADDR> \\
  [--rpc <ETH_RPC_URL>] \\
  [--api <GONKA_API_BASE_URL>] \\
  [--epoch <EPOCH_ID>] \\
  [--target-epoch <EPOCH_ID>] \\
  [--group-key <BASE64_OR_0X_HEX_GROUP_KEY>]

Example:
  node bridge-enable-normal-op.js \\
    --eth-key 0xYOUR_ETH_PRIVATE_KEY \\
    --bridge 0xYOUR_BRIDGE_CONTRACT

Environment:
  BRIDGE_ADDRESS may be used instead of --bridge.
  PRIVATE_KEY and SEPOLIA_RPC_URL may be used instead of --eth-key and --rpc.
`);
    process.exit(1);
}

function resolveBridgeAddress() {
    const explicit = getArg("--bridge") || process.env.BRIDGE_ADDRESS;
    if (explicit) {
        return explicit;
    }

    const addressFile = path.resolve(__dirname, "../../../proposals/ethereum-bridge-contact/bridge_address.txt");
    if (fs.existsSync(addressFile)) {
        return fs.readFileSync(addressFile, "utf8").trim();
    }

    return "";
}

function requestJson(url) {
    return new Promise((resolve, reject) => {
        const client = url.startsWith("https:") ? https : http;
        const req = client.get(url, (res) => {
            let body = "";
            res.setEncoding("utf8");
            res.on("data", chunk => body += chunk);
            res.on("end", () => {
                if (res.statusCode < 200 || res.statusCode >= 300) {
                    reject(new Error(`GET ${url} returned ${res.statusCode}: ${body.substring(0, 200)}`));
                    return;
                }
                try {
                    resolve(JSON.parse(body));
                } catch (e) {
                    reject(new Error(`GET ${url} returned invalid JSON: ${e.message}`));
                }
            });
        });
        req.on("error", reject);
        req.setTimeout(10000, () => req.destroy(new Error(`GET ${url} timed out`)));
    });
}

function decodeBytes(input, label) {
    if (!input) {
        throw new Error(`${label} is empty`);
    }

    let hex;
    if (input.startsWith("0x")) {
        hex = input.slice(2);
    } else if (/^[0-9a-fA-F]+$/.test(input) && input.length % 2 === 0) {
        hex = input;
    } else {
        hex = Buffer.from(input, "base64").toString("hex");
    }

    if (!/^[0-9a-fA-F]+$/.test(hex)) {
        throw new Error(`${label} is not valid hex or base64`);
    }
    return Buffer.from(hex, "hex");
}

function formatBytes(input, label, expectedBytes) {
    const bytes = decodeBytes(input, label);
    const hex = bytes.toString("hex");
    if (hex.length !== expectedBytes * 2) {
        throw new Error(`${label} must be ${expectedBytes} bytes, got ${hex.length / 2}`);
    }
    return `0x${hex}`;
}

async function formatG1Signature(input) {
    const bytes = decodeBytes(input, "validation signature");

    if (bytes.length === 128) {
        return `0x${bytes.toString("hex")}`;
    }
    if (bytes.length !== 48) {
        throw new Error(`validation signature must be 48 compressed bytes or 128 uncompressed bytes, got ${bytes.length}`);
    }

    const { bls12_381 } = await importBls12381();
    const point = bls12_381.G1.Point.fromBytes(bytes);
    const raw = Buffer.from(point.toBytes(false));

    if (raw.length !== 96) {
        throw new Error(`unexpected uncompressed G1 length from BLS library: ${raw.length}`);
    }

    const uncompressed = Buffer.alloc(128);
    raw.copy(uncompressed, 16, 0, 48);
    raw.copy(uncompressed, 64 + 16, 48, 96);

    return `0x${uncompressed.toString("hex")}`;
}

async function importBls12381() {
    try {
        return await import("@noble/curves/bls12-381.js");
    } catch (_) {
        const noblePath = path.join(
            __dirname,
            "../../../proposals/ethereum-bridge-contact/node_modules/@noble/curves/bls12-381.js"
        );
        return import(pathToFileURL(noblePath).href);
    }
}

async function formatGroupPublicKey(input) {
    const bytes = decodeBytes(input, "group public key");

    if (bytes.length === 256) {
        return `0x${bytes.toString("hex")}`;
    }
    if (bytes.length !== 96) {
        throw new Error(`group public key must be 96 compressed bytes or 256 uncompressed bytes, got ${bytes.length}`);
    }

    const { bls12_381 } = await importBls12381();
    const point = bls12_381.G2.Point.fromBytes(bytes);
    const raw = Buffer.from(point.toBytes(false));

    if (raw.length !== 192) {
        throw new Error(`unexpected uncompressed G2 length from BLS library: ${raw.length}`);
    }

    const uncompressed = Buffer.alloc(256);
    // Noble returns [X.c1, X.c0, Y.c1, Y.c0], 48 bytes each.
    // BridgeContract expects [X.c0, X.c1, Y.c0, Y.c1], each left-padded to 64 bytes.
    raw.copy(uncompressed, 0 * 64 + 16, 48, 96);
    raw.copy(uncompressed, 1 * 64 + 16, 0, 48);
    raw.copy(uncompressed, 2 * 64 + 16, 144, 192);
    raw.copy(uncompressed, 3 * 64 + 16, 96, 144);

    return `0x${uncompressed.toString("hex")}`;
}

async function getCurrentGonkaEpoch() {
    const data = await requestJson(`${gonkaApi}/chain-api/productscience/inference/inference/get_current_epoch`);
    const epoch = data.epoch == null ? "" : String(data.epoch);
    if (!epoch || epoch === "null") {
        throw new Error(`Could not read current epoch from ${gonkaApi}`);
    }
    return epoch;
}

async function getGonkaGroupKey(epoch) {
    const data = await requestJson(`${gonkaApi}/chain-api/productscience/inference/bls/epoch_data/${epoch}`);
    const groupKey = data.epoch_data && data.epoch_data.group_public_key;
    if (!groupKey) {
        throw new Error(`Could not read group public key for epoch ${epoch}`);
    }
    return groupKey;
}

async function getGonkaEpochData(epoch) {
    const data = await requestJson(`${gonkaApi}/chain-api/productscience/inference/bls/epoch_data/${epoch}`);
    const epochData = data.epoch_data;
    if (!epochData) {
        throw new Error(`Could not read epoch data for epoch ${epoch}`);
    }
    if (!epochData.group_public_key) {
        throw new Error(`Could not read group public key for epoch ${epoch}`);
    }
    return epochData;
}

async function main() {
    console.log("\n==================================================");
    console.log("Enable Bridge Normal Operation");
    console.log("==================================================\n");

    const provider = new ethers.JsonRpcProvider(ethRpcUrl);
    const network = await provider.getNetwork();
    const wallet = new ethers.Wallet(ethPrivateKey, provider);

    const abi = [
        "function owner() view returns (address)",
        "function getCurrentState() view returns (uint8)",
        "function getLatestEpochInfo() view returns (uint64 epochId, uint64 timestamp, bytes groupKey)",
        "function isValidEpoch(uint64 epochId) view returns (bool)",
        "function setGroupKey(uint64 epochId, bytes groupPublicKey) external",
        "function submitGroupKey(uint64 epochId, bytes groupPublicKey, bytes validationSig) external",
        "function resetToNormalOperation() external"
    ];
    const bridge = new ethers.Contract(bridgeAddress, abi, wallet);

    console.log(`Network:  chain ${network.chainId}`);
    console.log(`Bridge:   ${bridgeAddress}`);
    console.log(`Signer:   ${wallet.address}`);

    const code = await provider.getCode(bridgeAddress);
    if (code === "0x") {
        throw new Error(`No contract found at ${bridgeAddress}`);
    }

    const owner = await bridge.owner();
    console.log(`Owner:    ${owner}`);
    if (owner.toLowerCase() !== wallet.address.toLowerCase()) {
        throw new Error("Signer is not the BridgeContract owner");
    }

    const currentState = await bridge.getCurrentState();
    let latest = await bridge.getLatestEpochInfo();
    console.log(`State:    ${currentState === 0n ? "ADMIN_CONTROL" : "NORMAL_OPERATION"}`);
    console.log(`Latest:   epoch ${latest.epochId}`);

    if (currentState === 1n) {
        const targetEpoch = targetEpochArg ? BigInt(targetEpochArg) : null;
        if (targetEpoch == null || targetEpoch <= latest.epochId || await bridge.isValidEpoch(targetEpoch)) {
            console.log("\nBridge is already in NORMAL_OPERATION.");
            return;
        }

        console.log(`\nSyncing contract epochs through ${targetEpoch}...`);
        for (let epoch = latest.epochId + 1n; epoch <= targetEpoch; epoch++) {
            const epochData = await getGonkaEpochData(epoch.toString());
            if (!epochData.validation_signature) {
                throw new Error(`Epoch ${epoch} is missing validation_signature; cannot submit in NORMAL_OPERATION`);
            }

            const groupKey = await formatGroupPublicKey(epochData.group_public_key);
            const validationSig = await formatG1Signature(epochData.validation_signature);

            console.log(`Submitting epoch ${epoch}...`);
            const tx = await bridge.submitGroupKey(epoch, groupKey, validationSig);
            console.log(`Tx:       ${tx.hash}`);
            const receipt = await tx.wait();
            console.log(`Confirmed in block ${receipt.blockNumber}`);
        }

        console.log(`\nSUCCESS: Bridge epochs synced through ${targetEpoch}.`);
        return;
    }

    const epoch = epochArg || await getCurrentGonkaEpoch();
    const epochBigInt = BigInt(epoch);
    if (epochBigInt < 1n) {
        throw new Error(`Invalid epoch: ${epoch}`);
    }

    let hasEpoch = await bridge.isValidEpoch(epoch);
    if (!hasEpoch) {
        if (latest.epochId >= epochBigInt) {
            throw new Error(`Epoch ${epoch} is not stored, but latest contract epoch is already ${latest.epochId}`);
        }

        const groupKeyInput = groupKeyArg || await getGonkaGroupKey(epoch);
        const groupKey = await formatGroupPublicKey(groupKeyInput);

        console.log(`\nSubmitting group key for epoch ${epoch}...`);
        console.log(`GroupKey: ${groupKey.substring(0, 20)}...${groupKey.substring(groupKey.length - 10)}`);
        const setTx = await bridge.setGroupKey(epoch, groupKey);
        console.log(`Tx:       ${setTx.hash}`);
        const setReceipt = await setTx.wait();
        console.log(`Confirmed in block ${setReceipt.blockNumber}`);
        hasEpoch = await bridge.isValidEpoch(epoch);
        if (!hasEpoch) {
            throw new Error(`Epoch ${epoch} was not stored after setGroupKey`);
        }
    } else {
        console.log(`\nEpoch ${epoch} group key is already stored.`);
    }

    console.log("\nResetting bridge to NORMAL_OPERATION...");
    const resetTx = await bridge.resetToNormalOperation();
    console.log(`Tx:       ${resetTx.hash}`);
    const resetReceipt = await resetTx.wait();
    console.log(`Confirmed in block ${resetReceipt.blockNumber}`);

    const newState = await bridge.getCurrentState();
    if (newState !== 1n) {
        throw new Error(`State is still not NORMAL_OPERATION after reset (state=${newState})`);
    }

    console.log("\nSUCCESS: Bridge is now in NORMAL_OPERATION.");
}

main().catch(err => {
    console.error("\nFatal error:");
    console.error(err.message || err);
    process.exit(1);
});
