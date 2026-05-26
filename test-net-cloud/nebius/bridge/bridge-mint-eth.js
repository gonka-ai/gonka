const { ethers } = require("ethers");
const { execFileSync } = require("child_process");
const fs = require("fs");
const http = require("http");
const https = require("https");
const path = require("path");

// Parse all arguments as named flags
const args = process.argv.slice(2);
function getArg(flag) {
    const idx = args.indexOf(flag);
    return idx !== -1 ? args[idx + 1] : null;
}

const gonkaTxHash = getArg("--tx");
const defaultEthRpcUrl = process.env.SEPOLIA_RPC_URL || "https://ethereum-sepolia-rpc.publicnode.com";
const ethRpcUrl = getArg("--rpc") || defaultEthRpcUrl;
const ethPrivateKey = getArg("--eth-key");
const bridgeAddress = resolveBridgeAddress();
const gonkaNode = getArg("--node") || "http://89.169.111.79:8000/chain-rpc/";
const binaryPath = getArg("--binary") || "./inferenced";
const gonkaApi = getArg("--api") || new URL("/v1", gonkaNode).toString().replace(/\/$/, "");

if (!gonkaTxHash || !ethPrivateKey || !bridgeAddress) {
    console.log(`
Usage: node bridge-mint-eth.js \\
  --tx <GONKA_TX_HASH> \\
  --eth-key <ETH_PRIVATE_KEY> \\
  [--rpc <ETH_RPC_URL>] \\
  --bridge <BRIDGE_CONTRACT_ADDR> \\
  [--node <GONKA_RPC_URL>] \\
  [--api <GONKA_API_URL>] \\
  [--binary <INFERENCED_PATH>]

Example:
  node bridge-mint-eth.js \\
    --tx 1C58FABF... \\
    --rpc https://ethereum-sepolia-rpc.publicnode.com \\
    --eth-key 0xYOUR_ETH_PRIVATE_KEY \\
    --bridge 0xYOUR_BRIDGE_CONTRACT

Supports native GNK bridge-mint transactions and CW20 unwrap/withdraw transactions.

Environment:
  BRIDGE_ADDRESS may be used instead of --bridge.
  SEPOLIA_RPC_URL may be used instead of --rpc.
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

function maskRpcUrl(url) {
    return url.replace(/\/[a-zA-Z0-9_-]{16,}$/, "/***");
}

function getAttr(event, key, required = true) {
    const attr = event && event.attributes.find(a => a.key === key);
    if (!attr || attr.value == null) {
        if (required) {
            throw new Error(`Missing event attribute: ${key}`);
        }
        return "";
    }
    return String(attr.value).replace(/^"|"$/g, "");
}

function getTxEvents(txInfo) {
    if (Array.isArray(txInfo.events)) {
        return txInfo.events;
    }
    if (txInfo.tx_response && Array.isArray(txInfo.tx_response.events)) {
        return txInfo.tx_response.events;
    }
    return [];
}

function getTxMessages(txInfo) {
    if (txInfo.tx && txInfo.tx.body && Array.isArray(txInfo.tx.body.messages)) {
        return txInfo.tx.body.messages;
    }
    if (txInfo.tx_response && txInfo.tx_response.tx && txInfo.tx_response.tx.body && Array.isArray(txInfo.tx_response.tx.body.messages)) {
        return txInfo.tx_response.tx.body.messages;
    }
    return [];
}

function queryJson(args, options = {}) {
    return JSON.parse(execFileSync(binaryPath, args, options).toString());
}

function inferOperation(txInfo, bridgeEvent, wasmEvent) {
    if (wasmEvent) {
        return "withdraw";
    }

    const messages = getTxMessages(txInfo);
    const requestWithdrawal = messages.find(msg =>
        String(msg["@type"] || "").includes("MsgRequestBridgeWithdrawal")
    );
    if (requestWithdrawal) {
        return "withdraw";
    }

    const requestMint = messages.find(msg =>
        String(msg["@type"] || "").includes("MsgRequestBridgeMint")
    );
    if (requestMint) {
        return "mint";
    }

    const executeWithdraw = messages.find(msg =>
        msg["@type"] === "/cosmwasm.wasm.v1.MsgExecuteContract" &&
        msg.msg &&
        msg.msg.withdraw
    );
    if (executeWithdraw) {
        return "withdraw";
    }

    if (bridgeEvent) {
        return "mint";
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
        req.setTimeout(10000, () => {
            req.destroy(new Error(`GET ${url} timed out`));
        });
    });
}

async function assertJsonRpcProvider(provider, rpcUrl) {
    try {
        const network = await provider.getNetwork();
        console.log(`      > Ethereum RPC: ${maskRpcUrl(rpcUrl)} (chain ${network.chainId})`);
    } catch (e) {
        console.error("\n❌ Ethereum RPC Unavailable:");
        console.error(`Unable to connect to ${maskRpcUrl(rpcUrl)} as an Ethereum JSON-RPC endpoint.`);
        console.error("Use a Sepolia JSON-RPC URL, for example:");
        console.error("  --rpc https://ethereum-sepolia-rpc.publicnode.com");
        console.error("or set SEPOLIA_RPC_URL.");
        if (e && e.message) {
            console.error(`\nProvider error: ${e.message}`);
        }
        process.exit(1);
    }
}

async function fetchUncompressedSignature(requestIdHex) {
    const url = `${gonkaApi}/bls/signatures/${requestIdHex.replace(/^0x/, "")}`;
    const data = await requestJson(url);
    if (!data.uncompressed_signature_128) {
        throw new Error(`Missing uncompressed_signature_128 in ${url}`);
    }
    return data.uncompressed_signature_128;
}

function formatBytes(str, label, expectedBytes = null) {
    if (!str) return "0x";

    let hex;
    if (str.startsWith("0x")) {
        hex = str.slice(2);
    } else if (/^[0-9a-fA-F]+$/.test(str) && str.length % 2 === 0) {
        hex = str;
    } else {
        hex = Buffer.from(str, "base64").toString("hex");
    }

    if (expectedBytes != null && hex.length !== expectedBytes * 2) {
        throw new Error(`${label} must be ${expectedBytes} bytes, got ${hex.length / 2}`);
    }
    return `0x${hex}`;
}

function formatAddress(str, label) {
    const hex = formatBytes(str, label).slice(2);
    if (hex.length === 40) {
        return ethers.getAddress(`0x${hex}`);
    }
    if (hex.length === 64) {
        return ethers.getAddress(`0x${hex.slice(24)}`);
    }
    throw new Error(`${label} must be 20 or 32 bytes, got ${hex.length / 2}`);
}

function decodeContractError(error) {
    const errorData = error && (error.data || (error.info && error.info.error && error.info.error.data));
    const abi = [
        "error BridgeNotOperational()",
        "error InvalidEpoch()",
        "error RequestAlreadyProcessed()",
        "error InvalidSignature()",
        "error MustBeInAdminControl()",
        "error InvalidEpochSequence()",
        "error NoValidGenesisEpoch()",
        "error TimeoutNotReached()",
        "error InvalidAmount()"
    ];

    if (!errorData || errorData === "0x") {
        return "";
    }

    try {
        const iface = new ethers.Interface(abi);
        return iface.parseError(errorData).name;
    } catch (_) {
        return "";
    }
}

async function main() {
    console.log("\n==================================================");
    console.log("Bridge Ethereum Finalization Tool (Gonka -> Ethereum)");
    console.log("==================================================\n");

    const provider = new ethers.JsonRpcProvider(ethRpcUrl);
    await assertJsonRpcProvider(provider, ethRpcUrl);

    // 1. Get Request ID and Amount from Gonka Tx
    console.log(`[1/3] Querying Gonka Tx: ${gonkaTxHash}...`);
    let txInfo;
    try {
        txInfo = queryJson(["query", "tx", gonkaTxHash, "--node", gonkaNode, "--output", "json"]);
    } catch (e) {
        console.error(`Error querying transaction: ${e.message}`);
        process.exit(1);
    }

    // Find BLS Request ID (the cryptographic hash)
    const txEvents = getTxEvents(txInfo);
    const blsEvent = txEvents.find(e => e.type === "inference.bls.EventThresholdSigningRequested" || e.type.includes("EventThresholdSigningRequested"));
    const bridgeEvent = txEvents.find(e => e.type === "bridge_mint_requested");
    const wasmEvent = txEvents.find(e => e.type === "wasm" && getAttr(e, "method", false) === "withdraw");
    const operation = inferOperation(txInfo, bridgeEvent, wasmEvent);

    if (!blsEvent) {
        console.error("Error: Could not find BLS threshold signing request event in this transaction.");
        console.error(`Events seen: ${txEvents.map(e => e.type).join(", ") || "(none)"}`);
        process.exit(1);
    }

    if (!bridgeEvent && !wasmEvent) {
        console.error("Error: Could not find bridge_mint_requested or wasm withdraw event in this transaction.");
        console.error(`Events seen: ${txEvents.map(e => e.type).join(", ") || "(none)"}`);
        process.exit(1);
    }
    if (!operation) {
        console.error("Error: Could not infer whether this Gonka transaction should mint or withdraw on Ethereum.");
        process.exit(1);
    }

    const requestId = getAttr(blsEvent, "request_id");
    const epochId = getAttr(blsEvent, "current_epoch_id") || getAttr(bridgeEvent, "epoch_index", false);
    
    // Get amount and recipient from either bridge event or wasm event
    const amount = getAttr(bridgeEvent || wasmEvent, "amount");
    const recipient = getAttr(bridgeEvent || wasmEvent, "destination_address");
    
    console.log(`      > Request ID: ${requestId}`);
    console.log(`      > Epoch:      ${epochId}`);
    console.log(`      > Recipient:  ${recipient}`);
    console.log(`      > Amount:     ${amount} base units`);
    console.log(`      > Operation:  ${operation}`);

    // 2. Fetch BLS Signature
    console.log("\n[2/3] Fetching BLS signature from Gonka...");

    let signature = "";
    let signingRequest = null;
    while (!signature) {
        try {
            const history = queryJson(["query", "bls", "signing-history", "--output", "json", "--node", gonkaNode], { stdio: ['inherit', 'pipe', 'ignore'] });
            const request = history.signing_requests.find(r => r.request_id === requestId);

            if (request && request.status === "THRESHOLD_SIGNING_STATUS_COMPLETED") {
                signingRequest = request;
                signature = request.final_signature;
            }

            if (!signature) throw new Error("Pending");
        } catch (e) {
            process.stdout.write(".");
            await new Promise(r => setTimeout(r, 3000));
        }
    }
    const formattedRequestId = formatBytes(requestId, "requestId", 32);
    let formattedSignature;
    try {
        formattedSignature = formatBytes(signature, "signature", 128);
    } catch (e) {
        const compressedSignature = formatBytes(signature, "compressed signature", 48);
        console.log(`\n      > Compressed signature obtained: ${compressedSignature.substring(0, 34)}...`);
        console.log("      > Fetching 128-byte EIP-2537 signature...");
        const uncompressedSignature = await fetchUncompressedSignature(formattedRequestId);
        formattedSignature = formatBytes(uncompressedSignature, "uncompressed signature", 128);
    }
    console.log(`\n      > Signature obtained: ${formattedSignature.substring(0, 34)}...`);

    // 3. Submit to Ethereum
    console.log("\n[3/3] Submitting to Ethereum...");

    const wallet = new ethers.Wallet(ethPrivateKey, provider);

    const abi = [
        "function mintWithSignature((uint64 epochId, bytes32 requestId, address recipient, uint256 amount, bytes signature) cmd) external",
        "function withdraw((uint64 epochId, bytes32 requestId, address recipient, address tokenContract, uint256 amount, bytes signature) cmd) external",
        "function getCurrentState() view returns (uint8)",
        "function getLatestEpochInfo() view returns (uint64 epochId, uint64 timestamp, bytes groupKey)",
        "function isValidEpoch(uint64 epochId) view returns (bool)",
        "function isRequestProcessed(uint64 epochId, bytes32 requestId) view returns (bool)"
    ];
    const contract = new ethers.Contract(bridgeAddress, abi, wallet);

    console.log(`      > Bridge:     ${bridgeAddress}`);
    console.log(`      > Epoch:      ${epochId}`);
    console.log(`      > Recipient:  ${recipient}`);
    console.log(`      > Request ID: ${formattedRequestId.substring(0, 20)}...`);

    const command = {
        epochId,
        requestId: formattedRequestId,
        recipient,
        amount,
        signature: formattedSignature
    };
    if (operation === "withdraw") {
        if (!signingRequest || !Array.isArray(signingRequest.data) || signingRequest.data.length < 5) {
            throw new Error("Could not derive tokenContract for withdraw from BLS signing request data");
        }
        command.tokenContract = formatAddress(signingRequest.data[4], "tokenContract");
        console.log(`      > Token:      ${command.tokenContract}`);
    }

    const currentState = await contract.getCurrentState();
    if (currentState !== 1n) {
        throw new Error(`Bridge contract is not in NORMAL_OPERATION state (state=${currentState})`);
    }
    if (!await contract.isValidEpoch(epochId)) {
        const latest = await contract.getLatestEpochInfo();
        throw new Error(`Bridge contract does not have a group key for epoch ${epochId} (latest contract epoch=${latest.epochId})`);
    }
    if (await contract.isRequestProcessed(epochId, formattedRequestId)) {
        throw new Error(`Request ${formattedRequestId} was already processed for epoch ${epochId}`);
    }

    console.log(`      > Sending transaction...`);

    try {
        const tx = operation === "withdraw"
            ? await contract.withdraw(command)
            : await contract.mintWithSignature(command);
        console.log(`      > Tx Hash: ${tx.hash}`);
        console.log(`      > Waiting for confirmation...`);
        const receipt = await tx.wait();
        const action = operation === "withdraw" ? "withdrawn" : "minted";
        console.log(`\n✅ SUCCESS! Tokens ${action} on Ethereum in block ${receipt.blockNumber}.`);
    } catch (e) {
        console.error(`\n❌ Ethereum Transaction Failed:`);
        console.error(e.message);
        const decoded = decodeContractError(e);
        if (decoded) {
            console.error(`Decoded contract error: ${decoded}`);
        } else if (e.data === "0x") {
            console.error("No revert data was returned. Check that the address is the expected BridgeContract on this network.");
        }
        process.exit(1);
    }
}

main().catch(err => {
    console.error("\n❌ Fatal Error:");
    console.error(err);
    process.exit(1);
});
